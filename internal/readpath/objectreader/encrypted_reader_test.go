package objectreader

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/crypto/randomaccess"
	"github.com/PRIEYAN/ButterDB/internal/crypto/stream"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

func TestOpenerReadsEncryptedFileAsPlaintext(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0x31}, 32)
	plaintext := patternedPlaintext(2*encryptedTestChunk + 37)
	ciphertext := encryptFixture(t, plaintext, masterKey)
	tg := newFakeTelegram(map[int64][]byte{10: ciphertext})

	providedKey := append([]byte(nil), masterKey...)
	var keyCalls atomic.Int32
	opener := newDefaultOpener(t, tg, keyFunc(func(ctx context.Context, channelID int64, keyID string) (DEK, error) {
		keyCalls.Add(1)
		if channelID != testChannelID || keyID != testKeyID {
			t.Errorf("key request = (%d, %q), want (%d, %q)", channelID, keyID, testChannelID, testKeyID)
		}
		return newTestDEK(providedKey), nil
	}))

	reader := mustOpenReader(t, opener, encryptedMeta(10, ciphertext, len(plaintext)))
	if keyCalls.Load() != 1 {
		t.Fatalf("key calls = %d, want 1", keyCalls.Load())
	}
	if !zeroed(providedKey) {
		t.Fatal("Open retained the provider-owned DEK bytes")
	}
	if got := reader.Size(); got != int64(len(plaintext)) {
		t.Fatalf("Size = %d, want %d", got, len(plaintext))
	}

	got := make([]byte, encryptedTestChunk+29)
	off := int64(encryptedTestChunk - 11)
	n, readErr := reader.ReadAt(context.Background(), got, off)
	if readErr != nil || n != len(got) || !bytes.Equal(got, plaintext[off:off+int64(n)]) {
		t.Fatalf("ReadAt = n %d err %v matching=%v", n, readErr, bytes.Equal(got[:n], plaintext[off:off+int64(n)]))
	}

	tail := make([]byte, 100)
	n, readErr = reader.ReadAt(context.Background(), tail, int64(len(plaintext)-7))
	if n != 7 || !errors.Is(readErr, io.EOF) || !bytes.Equal(tail[:n], plaintext[len(plaintext)-7:]) {
		t.Fatalf("tail ReadAt = n %d err %v data=%x", n, readErr, tail[:n])
	}
}

func TestEncryptedRangeAcrossChunks(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0x42}, 32)
	plaintext := patternedPlaintext(4*encryptedTestChunk + 19)
	ciphertext := encryptFixture(t, plaintext, masterKey)
	tg := newFakeTelegram(map[int64][]byte{10: ciphertext})
	opener := newDefaultOpener(t, tg, fixedKey(masterKey))
	reader := mustOpenReader(t, opener, encryptedMeta(10, ciphertext, len(plaintext)))

	tests := []struct {
		name   string
		offset int64
		length int
	}{
		{name: "inside one chunk", offset: 100, length: 500},
		{name: "ends exactly on a chunk boundary", offset: encryptedTestChunk - 64, length: 64},
		{name: "starts exactly on a chunk boundary", offset: encryptedTestChunk, length: 64},
		{name: "straddles one boundary", offset: encryptedTestChunk - 17, length: 40},
		{name: "spans three chunks", offset: encryptedTestChunk - 17, length: 2*encryptedTestChunk + 36},
		{name: "reaches the final partial chunk", offset: 3*encryptedTestChunk + 5, length: encryptedTestChunk + 5},
		{name: "whole object", offset: 0, length: len(plaintext)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make([]byte, tt.length)
			n, err := reader.ReadAt(context.Background(), got, tt.offset)
			end := tt.offset + int64(tt.length)
			if end > int64(len(plaintext)) {
				end = int64(len(plaintext))
			}
			want := plaintext[tt.offset:end]
			if n != len(want) || !bytes.Equal(got[:n], want) {
				t.Fatalf("ReadAt n=%d err=%v, bytes match=%v", n, err, bytes.Equal(got[:n], want))
			}
			if n < len(got) && !errors.Is(err, io.EOF) {
				t.Fatalf("short ReadAt err = %v, want io.EOF", err)
			}
			if n == len(got) && err != nil {
				t.Fatalf("full ReadAt err = %v, want nil", err)
			}
		})
	}
}

func TestOpenerRequestsKeyOnlyForEncryptedFilesAfterResolvingBodies(t *testing.T) {
	plainBody := []byte("public")
	masterKey := bytes.Repeat([]byte{0x53}, 32)
	privateBody := []byte("private")
	ciphertext := encryptFixture(t, privateBody, masterKey)

	tg := newFakeTelegram(map[int64][]byte{10: plainBody, 20: ciphertext})
	var keyCalls atomic.Int32
	opener := newDefaultOpener(t, tg, keyFunc(func(context.Context, int64, string) (DEK, error) {
		keyCalls.Add(1)
		resolvedPrivate := false
		for _, msgID := range tg.resolvedMessages() {
			resolvedPrivate = resolvedPrivate || msgID == 20
		}
		if !resolvedPrivate {
			t.Error("key requested before the encrypted Telegram body was resolved")
		}
		return newTestDEK(append([]byte(nil), masterKey...)), nil
	}))

	mustOpenReader(t, opener, plainMeta(10, plainBody))
	if keyCalls.Load() != 0 {
		t.Fatalf("plaintext Open key calls = %d, want 0", keyCalls.Load())
	}
	mustOpenReader(t, opener, encryptedMeta(20, ciphertext, len(privateBody)))
	if keyCalls.Load() != 1 {
		t.Fatalf("encrypted Open key calls = %d, want 1", keyCalls.Load())
	}
}

func TestOpenerRejectsUnavailableWrongAndTamperedEncryptedKeys(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0x64}, 32)
	plaintext := patternedPlaintext(encryptedTestChunk + 9)
	ciphertext := encryptFixture(t, plaintext, masterKey)
	meta := encryptedMeta(10, ciphertext, len(plaintext))
	tg := newFakeTelegram(map[int64][]byte{10: ciphertext})

	t.Run("provider error", func(t *testing.T) {
		sentinel := errors.New("keyring locked")
		returnedKey := append([]byte(nil), masterKey...)
		dek := newTestDEK(returnedKey)
		opener := newDefaultOpener(t, tg, keyFunc(func(context.Context, int64, string) (DEK, error) { return dek, sentinel }))
		_, openErr := opener.Open(context.Background(), meta)
		if !errors.Is(openErr, sentinel) || !errors.Is(openErr, errs.ErrKeyUnavailable) {
			t.Fatalf("Open error = %v, want wrapped sentinel and ErrKeyUnavailable", openErr)
		}
		if !zeroed(returnedKey) {
			t.Fatal("Open retained key bytes returned alongside a provider error")
		}
	})

	t.Run("empty key", func(t *testing.T) {
		opener := newDefaultOpener(t, tg, keyFunc(func(context.Context, int64, string) (DEK, error) {
			return newTestDEK(nil), nil
		}))
		if _, openErr := opener.Open(context.Background(), meta); !errors.Is(openErr, errs.ErrKeyUnavailable) {
			t.Fatalf("Open error = %v, want ErrKeyUnavailable", openErr)
		}
	})

	t.Run("wrong key", func(t *testing.T) {
		opener := newDefaultOpener(t, tg, fixedKey(bytes.Repeat([]byte{0x65}, 32)))
		if _, openErr := opener.Open(context.Background(), meta); !errors.Is(openErr, stream.ErrAuthFailed) {
			t.Fatalf("Open error = %v, want stream.ErrAuthFailed", openErr)
		}
	})

	t.Run("tampered read chunk", func(t *testing.T) {
		tampered := append([]byte(nil), ciphertext...)
		tampered[format.HeaderLen+3] ^= 0x80
		tamperedTG := newFakeTelegram(map[int64][]byte{10: tampered})
		opener := newDefaultOpener(t, tamperedTG, fixedKey(masterKey))
		reader := mustOpenReader(t, opener, meta)
		if n, readErr := reader.ReadAt(context.Background(), make([]byte, 1), 0); n != 0 || !errors.Is(readErr, stream.ErrAuthFailed) {
			t.Fatalf("ReadAt tampered chunk = n %d err %v", n, readErr)
		}
	})
}

func TestEncryptedOpenAndReaderLifecycleAreCancellationSafe(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0x75}, 32)
	plaintext := []byte("lifecycle")
	ciphertext := encryptFixture(t, plaintext, masterKey)
	meta := encryptedMeta(10, ciphertext, len(plaintext))
	tg := newFakeTelegram(map[int64][]byte{10: ciphertext})

	started := make(chan struct{})
	blocked := newDefaultOpener(t, tg, keyFunc(func(ctx context.Context, _ int64, _ string) (DEK, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	result := make(chan error, 1)
	go func() {
		_, openErr := blocked.Open(context.Background(), meta)
		result <- openErr
	}()
	<-started
	if err := blocked.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if openErr := <-result; !errors.Is(openErr, ErrClosed) {
		t.Fatalf("Open overlapping Close error = %v, want ErrClosed", openErr)
	}

	opener := newDefaultOpener(t, tg, fixedKey(masterKey))
	reader := mustOpenReader(t, opener, meta)
	if err := reader.Close(); err != nil {
		t.Fatalf("Reader.Close: %v", err)
	}
	if n, readErr := reader.ReadAt(context.Background(), make([]byte, 1), 0); n != 0 || !errors.Is(readErr, ErrReaderClosed) {
		t.Fatalf("closed encrypted Reader.ReadAt = n %d err %v", n, readErr)
	}
}

func TestOpenerCloseClosesPublishedEncryptedReaders(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0x87}, 32)
	plaintext := []byte("close with opener")
	ciphertext := encryptFixture(t, plaintext, masterKey)
	tg := newFakeTelegram(map[int64][]byte{10: ciphertext})
	opener := newDefaultOpener(t, tg, fixedKey(masterKey))
	reader := mustOpenReader(t, opener, encryptedMeta(10, ciphertext, len(plaintext)))
	decryptor := reader.decryptor
	if decryptor == nil {
		t.Fatal("encrypted Reader has no decryptor")
	}

	if err := opener.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n, readErr := decryptor.ReadAt(context.Background(), make([]byte, 1), 0); n != 0 || !errors.Is(readErr, randomaccess.ErrDecryptorClosed) {
		t.Fatalf("decryptor after Opener.Close = n %d err %v, want ErrDecryptorClosed", n, readErr)
	}
	if n, readErr := reader.ReadAt(context.Background(), make([]byte, 1), 0); n != 0 || !errors.Is(readErr, ErrClosed) {
		t.Fatalf("Reader after Opener.Close = n %d err %v, want ErrClosed", n, readErr)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Reader.Close after Opener.Close: %v", err)
	}
}

func TestOpenerRejectsUnsupportedEncryptedVersionBeforeRequestingKey(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0x86}, 32)
	plaintext := []byte("version")
	ciphertext := encryptFixture(t, plaintext, masterKey)
	meta := encryptedMeta(10, ciphertext, len(plaintext))
	meta.EncryptionVersion = 2
	tg := newFakeTelegram(map[int64][]byte{10: ciphertext})
	var keyCalls atomic.Int32
	opener := newDefaultOpener(t, tg, keyFunc(func(context.Context, int64, string) (DEK, error) {
		keyCalls.Add(1)
		return newTestDEK(append([]byte(nil), masterKey...)), nil
	}))

	if _, openErr := opener.Open(context.Background(), meta); !errors.Is(openErr, ErrEncryptedUnsupported) {
		t.Fatalf("Open error = %v, want ErrEncryptedUnsupported", openErr)
	}
	if keyCalls.Load() != 0 {
		t.Fatalf("key calls = %d, want 0", keyCalls.Load())
	}
	if got := tg.resolvedMessages(); len(got) != 0 {
		t.Fatalf("resolved messages %v before metadata validation, want none", got)
	}
}

func TestEncryptedMetadataRejectsPlaintextBeyondTDE1Capacity(t *testing.T) {
	err := validateMeta(object.ObjectMeta{
		StoredSize:        1<<63 - 1,
		PlaintextSize:     1<<63 - 1,
		BodyMsgID:         1,
		KeyID:             testKeyID,
		Encrypted:         true,
		EncryptionVersion: 1,
	})
	if !errors.Is(err, format.ErrPlaintextTooLarge) {
		t.Fatalf("validateMeta error = %v, want format.ErrPlaintextTooLarge", err)
	}
}

func TestEncryptedPlaintextSizeMustMatchHeader(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0x91}, 32)
	plaintext := patternedPlaintext(encryptedTestChunk + 3)
	ciphertext := encryptFixture(t, plaintext, masterKey)
	padded := append(append([]byte(nil), ciphertext...), 0)
	meta := encryptedMeta(10, padded, len(plaintext)+1)
	tg := newFakeTelegram(map[int64][]byte{10: padded})
	opener := newDefaultOpener(t, tg, fixedKey(masterKey))

	if _, err := opener.Open(context.Background(), meta); !errors.Is(err, randomaccess.ErrCiphertextSize) {
		t.Fatalf("Open error = %v, want ErrCiphertextSize", err)
	}
}

func TestKeyClearedOnClose(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0xa7}, 32)
	plaintext := patternedPlaintext(encryptedTestChunk + 11)
	ciphertext := encryptFixture(t, plaintext, masterKey)
	tg := newFakeTelegram(map[int64][]byte{10: ciphertext})

	providedKey := append([]byte(nil), masterKey...)
	dek := newTestDEK(providedKey)
	opener := newDefaultOpener(t, tg, keyFunc(func(context.Context, int64, string) (DEK, error) { return dek, nil }))
	reader := mustOpenReader(t, opener, encryptedMeta(10, ciphertext, len(plaintext)))
	decryptor := reader.decryptor

	if err := reader.Close(); err != nil {
		t.Fatalf("Reader.Close: %v", err)
	}
	if !zeroed(providedKey) {
		t.Fatal("DEK copy still holds key bytes after the reader closed")
	}
	if dek.cleared < 1 {
		t.Fatal("Clear was never called on the provided DEK")
	}
	if n, err := decryptor.ReadAt(context.Background(), make([]byte, 1), 0); n != 0 || !errors.Is(err, randomaccess.ErrDecryptorClosed) {
		t.Fatalf("decryptor after Reader.Close = n %d err %v, want ErrDecryptorClosed", n, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("second Reader.Close: %v", err)
	}
}

func TestKeyClearedWhenOpenFails(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0xb8}, 32)
	plaintext := []byte("fails to authenticate")
	ciphertext := encryptFixture(t, plaintext, masterKey)
	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 0x01
	tg := newFakeTelegram(map[int64][]byte{10: tampered})

	providedKey := append([]byte(nil), masterKey...)
	opener := newDefaultOpener(t, tg, keyFunc(func(context.Context, int64, string) (DEK, error) {
		return newTestDEK(providedKey), nil
	}))
	if _, err := opener.Open(context.Background(), encryptedMeta(10, tampered, len(plaintext))); !errors.Is(err, stream.ErrAuthFailed) {
		t.Fatalf("Open error = %v, want stream.ErrAuthFailed", err)
	}
	if !zeroed(providedKey) {
		t.Fatal("failed Open left DEK bytes in memory")
	}
}
