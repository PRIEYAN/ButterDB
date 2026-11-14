package rangeio

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

type cdnServer struct {
	mu          sync.Mutex
	plain       []byte
	key         []byte
	iv          []byte
	corruptHash bool
	dropHashes  bool
	needsReup   int
	reuploads   int
	cdnReads    []int64
	hashCalls   int
}

func newCDNServer() *cdnServer {
	plain := make([]byte, 3*4096)
	for i := range plain {
		plain[i] = byte(i*13 + i/4096)
	}
	return &cdnServer{plain: plain, key: bytes.Repeat([]byte{7}, 32), iv: bytes.Repeat([]byte{3}, 16)}
}

func (s *cdnServer) encrypt(offset int64, plain []byte) []byte {
	block, _ := aes.NewCipher(s.key)
	iv := append([]byte(nil), s.iv...)
	binary.BigEndian.PutUint32(iv[len(iv)-4:], uint32(offset/16))
	out := make([]byte, len(plain))
	cipher.NewCTR(block, iv).XORKeyStream(out, plain)
	return out
}

func (s *cdnServer) handle(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch request := input.(type) {
	case *tg.UploadGetFileRequest:
		output.(*tg.UploadFileBox).File = &tg.UploadFileCDNRedirect{
			DCID: 203, FileToken: []byte("token"), EncryptionKey: s.key, EncryptionIv: s.iv,
		}
	case *tg.UploadGetCDNFileRequest:
		if s.needsReup > 0 {
			s.needsReup--
			output.(*tg.UploadCDNFileBox).CdnFile = &tg.UploadCDNFileReuploadNeeded{RequestToken: []byte("req")}
			return nil
		}
		s.cdnReads = append(s.cdnReads, request.Offset)
		end := min(request.Offset+int64(request.Limit), int64(len(s.plain)))
		output.(*tg.UploadCDNFileBox).CdnFile = &tg.UploadCDNFile{Bytes: s.encrypt(request.Offset, s.plain[request.Offset:end])}
	case *tg.UploadReuploadCDNFileRequest:
		s.reuploads++
		output.(*tg.FileHashVector).Elems = nil
	case *tg.UploadGetCDNFileHashesRequest:
		s.hashCalls++
		if s.dropHashes {
			return nil
		}
		var hashes []tg.FileHash
		for offset := request.Offset; offset < int64(len(s.plain)); offset += 4096 {
			sum := sha256.Sum256(s.plain[offset : offset+4096])
			if s.corruptHash {
				sum[0] ^= 0xff
			}
			hashes = append(hashes, tg.FileHash{Offset: offset, Limit: 4096, Hash: sum[:]})
		}
		output.(*tg.FileHashVector).Elems = hashes
	default:
		return errors.New("unexpected request")
	}
	return nil
}

func TestRangeReadsVerifyCDNHashes(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*cdnServer)
		offset    int64
		length    int
		wantErr   bool
		reuploads int
	}{
		{name: "verified bytes are decrypted", offset: 4096, length: 4096},
		{name: "partial block verified through the rounded hash range", offset: 4096, length: 100},
		{name: "hash mismatch is rejected", configure: func(s *cdnServer) { s.corruptHash = true }, offset: 0, length: 4096, wantErr: true},
		{name: "missing hashes are rejected", configure: func(s *cdnServer) { s.dropHashes = true }, offset: 0, length: 4096, wantErr: true},
		{name: "one reupload request is honoured", configure: func(s *cdnServer) { s.needsReup = 1 }, offset: 0, length: 4096, reuploads: 1},
		{name: "repeated reupload request is refused", configure: func(s *cdnServer) { s.needsReup = 2 }, offset: 0, length: 4096, wantErr: true, reuploads: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newCDNServer()
			if test.configure != nil {
				test.configure(server)
			}
			h := newReaderHarness(t, server.handle)
			dst := make([]byte, test.length)
			n, err := h.reader.ReadDocumentRange(context.Background(), documentRef(), test.offset, dst)
			if test.wantErr {
				if err == nil || n != 0 {
					t.Fatalf("read = (%d, %v), want a rejection", n, err)
				}
			} else if err != nil || n != test.length || !bytes.Equal(dst, server.plain[test.offset:test.offset+int64(test.length)]) {
				t.Fatalf("read = (%d, %v), want the plaintext range", n, err)
			}
			if server.reuploads != test.reuploads {
				t.Fatalf("reuploads = %d, want %d", server.reuploads, test.reuploads)
			}
			cdnRoutes := 0
			for _, route := range h.invoker.routes() {
				if route.media && route.dc == 203 {
					cdnRoutes++
				}
			}
			if cdnRoutes == 0 {
				t.Fatalf("no call reached the cdn data center: %+v", h.invoker.routes())
			}
		})
	}
}

func TestDecryptCDNBytesUsesBlockCounterFromOffset(t *testing.T) {
	server := newCDNServer()
	redirect := &tg.UploadFileCDNRedirect{EncryptionKey: server.key, EncryptionIv: server.iv}
	for _, offset := range []int64{0, 16, 4096, 8192} {
		sealed := server.encrypt(offset, server.plain[offset:offset+64])
		got, err := decryptCDNBytes(redirect, sealed, offset)
		if err != nil || !bytes.Equal(got, server.plain[offset:offset+64]) {
			t.Fatalf("offset %d: decrypt = %v", offset, err)
		}
	}
	if _, err := decryptCDNBytes(&tg.UploadFileCDNRedirect{EncryptionKey: server.key, EncryptionIv: []byte("short")}, nil, 0); err == nil {
		t.Fatal("a short iv must be rejected")
	}
	if _, err := decryptCDNBytes(&tg.UploadFileCDNRedirect{EncryptionKey: []byte("bad")}, nil, 0); err == nil {
		t.Fatal("a bad key must be rejected")
	}
}
