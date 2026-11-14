package phase03

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/crypto/randomaccess"
	"github.com/PRIEYAN/ButterDB/internal/crypto/stream"
)

type bytesSource []byte

func (b bytesSource) ReadAt(_ context.Context, dst []byte, offset int64) (int, error) {
	if offset >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(dst, b[offset:])
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}

func TestCiphertextTamperAndTruncationFailAuthentication(t *testing.T) {
	h := newHarness(t)
	encfg, kid, original := h.encfgOp(5, channelA)
	wrap, check := encfg.WrappedKey, encfg.KeyCheck
	dek, err := h.ring.UnwrapDEK(wrap, check, channelA, kid)
	if err != nil {
		t.Fatalf("unwrap dek: %v", err)
	}
	t.Cleanup(dek.Clear)
	if !bytes.Equal(dek.Bytes(), original.Bytes()) {
		t.Fatal("unwrapped dek differs from the wrapped dek")
	}

	plain := make([]byte, 2*format.ChunkSize+977)
	if _, err := newSeqReader("plaintext").Read(plain); err != nil {
		t.Fatalf("fill plaintext: %v", err)
	}
	params, err := stream.NewFileParams(newSeqReader("file params"))
	if err != nil {
		t.Fatalf("file params: %v", err)
	}
	var sealed bytes.Buffer
	if err := stream.EncryptStream(&sealed, bytes.NewReader(plain), dek.Bytes(), int64(len(plain)), params.Salt, params.NoncePrefix); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	valid := sealed.Bytes()
	if int64(len(valid)) != format.CiphertextSize(int64(len(plain))) {
		t.Fatalf("ciphertext length %d, want %d", len(valid), format.CiphertextSize(int64(len(plain))))
	}

	var out bytes.Buffer
	n, err := stream.DecryptStream(&out, bytes.NewReader(valid), dek.Bytes())
	if err != nil || n != int64(len(plain)) || !bytes.Equal(out.Bytes(), plain) {
		t.Fatalf("honest decrypt = %d, %v", n, err)
	}
	opened, err := randomaccess.NewRandomAccessDecryptor(context.Background(), bytesSource(valid), int64(len(valid)), dek.Bytes())
	if err != nil {
		t.Fatalf("honest open: %v", err)
	}
	got := make([]byte, 64)
	if read, err := opened.ReadAt(context.Background(), got, int64(format.ChunkSize)-10); err != nil || !bytes.Equal(got[:read], plain[format.ChunkSize-10:format.ChunkSize+54]) {
		t.Fatalf("honest random read = %d, %v", read, err)
	}
	_ = opened.Close()

	stride := format.ChunkSize + format.TagLen
	flipAt := map[string]int{
		"first chunk":  format.HeaderLen + 5,
		"middle chunk": format.HeaderLen + stride + 5,
		"final chunk":  len(valid) - format.TagLen - 1,
		"final tag":    len(valid) - 1,
	}
	for name, at := range flipAt {
		t.Run("flipped byte in the "+name, func(t *testing.T) {
			tampered := append([]byte(nil), valid...)
			tampered[at] ^= 0x01
			var sink bytes.Buffer
			if _, err := stream.DecryptStream(&sink, bytes.NewReader(tampered), dek.Bytes()); !errors.Is(err, stream.ErrAuthFailed) {
				t.Fatalf("DecryptStream error = %v, want ErrAuthFailed", err)
			}
			decryptor, err := randomaccess.NewRandomAccessDecryptor(context.Background(), bytesSource(tampered), int64(len(tampered)), dek.Bytes())
			if err == nil {
				defer decryptor.Close()
				chunk := (at - format.HeaderLen) / stride
				buf := make([]byte, 16)
				n, err := decryptor.ReadAt(context.Background(), buf, int64(chunk*format.ChunkSize))
				if n != 0 || !errors.Is(err, stream.ErrAuthFailed) {
					t.Fatalf("random read of tampered chunk = %d, %v; want 0, ErrAuthFailed", n, err)
				}
				return
			}
			if !errors.Is(err, stream.ErrAuthFailed) {
				t.Fatalf("open error = %v, want ErrAuthFailed", err)
			}
		})
	}

	for _, cut := range []int{1, format.TagLen, stride, stride + 100} {
		t.Run(fmt.Sprintf("truncated tail cut %d", cut), func(t *testing.T) {
			truncated := valid[:len(valid)-cut]
			var sink bytes.Buffer
			if _, err := stream.DecryptStream(&sink, bytes.NewReader(truncated), dek.Bytes()); err == nil {
				t.Fatalf("cut %d: DecryptStream accepted a truncated stream", cut)
			}
			if d, err := randomaccess.NewRandomAccessDecryptor(context.Background(), bytesSource(truncated), int64(len(truncated)), dek.Bytes()); err == nil {
				_ = d.Close()
				t.Fatalf("cut %d: random access opened a truncated stream", cut)
			}
			if d, err := randomaccess.NewRandomAccessDecryptor(context.Background(), bytesSource(truncated), int64(len(valid)), dek.Bytes()); err == nil {
				_ = d.Close()
				t.Fatalf("cut %d: random access opened a truncated stream with the declared size", cut)
			}
		})
	}

	t.Run("another bucket's dek cannot decrypt", func(t *testing.T) {
		if _, err := h.ring.UnwrapDEK(wrap, check, channelB, kid); err == nil {
			t.Fatal("dek unwrapped under another bucket channel")
		}
	})
}
