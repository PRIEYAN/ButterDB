package sigv4

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	maxChunkHeaderLine   = 4 << 10
	maxChunkHexDigits    = 16
	maxChunkSize         = 16 << 20
	maxTrailerBytes      = 16 << 10
	chunkSignaturePrefix = "chunk-signature="
	trailerSignatureName = "x-amz-trailer-signature"
	chunkStringToSignTag = "AWS4-HMAC-SHA256-PAYLOAD"
	trailerStringToSign  = "AWS4-HMAC-SHA256-TRAILER"
)

type chunkParams struct {
	signedChunks  bool
	signedTrailer bool
	trailerName   string
	decodedLength int64
	signingKey    []byte
	seed          string
	requestTime   time.Time
	region        string
}

type chunkDecoder struct {
	src            *bufio.Reader
	params         chunkParams
	amzDate        string
	scope          string
	previous       string
	consumed       int64
	chunk          bytes.Buffer
	pending        []byte
	remaining      int64
	needTerminator bool
	finished       bool
	err            error
	trailerValue   string
}

func newChunkDecoder(src io.Reader, params chunkParams) *chunkDecoder {
	requestTime := params.requestTime.UTC()
	return &chunkDecoder{
		src:      bufio.NewReader(src),
		params:   params,
		amzDate:  requestTime.Format(timeLayout),
		scope:    credentialScopeText(requestTime.Format(dateLayout), params.region),
		previous: params.seed,
	}
}

func (d *chunkDecoder) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if d.err != nil {
			return 0, d.err
		}
		if len(d.pending) > 0 {
			n := copy(p, d.pending)
			d.pending = d.pending[n:]
			return n, nil
		}
		if d.remaining > 0 {
			return d.readUnsignedData(p)
		}
		if d.finished {
			return 0, io.EOF
		}
		d.err = d.advance()
	}
}

func (d *chunkDecoder) readUnsignedData(p []byte) (int, error) {
	if int64(len(p)) > d.remaining {
		p = p[:d.remaining]
	}
	n, err := d.src.Read(p)
	d.remaining -= int64(n)
	d.consumed += int64(n)
	if d.remaining == 0 {
		d.needTerminator = true
	}
	if err != nil && d.remaining > 0 {
		d.err = readFailure(err)
		if n == 0 {
			return 0, d.err
		}
	}
	return n, nil
}

func readFailure(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return newError(ReasonIncompleteBody)
	}
	return fmt.Errorf("sigv4: read body: %w", err)
}

func (d *chunkDecoder) advance() error {
	if d.needTerminator {
		if err := d.expectCRLF(); err != nil {
			return err
		}
		d.needTerminator = false
	}
	line, err := d.readLine(maxChunkHeaderLine)
	if err != nil {
		return err
	}
	size, signature, err := d.parseChunkHeader(line)
	if err != nil {
		return err
	}
	if d.params.decodedLength >= 0 && d.consumed+size > d.params.decodedLength {
		return newError(ReasonIncompleteBody)
	}
	switch {
	case size == 0:
		return d.finish(signature)
	case d.params.signedChunks:
		return d.loadSignedChunk(size, signature)
	}
	d.remaining = size
	return nil
}

func (d *chunkDecoder) parseChunkHeader(line string) (int64, string, error) {
	sizeText, extension, hasExtension := strings.Cut(line, ";")
	if len(sizeText) == 0 || len(sizeText) > maxChunkHexDigits {
		return 0, "", newError(ReasonChunkFraming)
	}
	size, err := strconv.ParseUint(sizeText, 16, 64)
	if err != nil || size > maxChunkSize {
		return 0, "", newError(ReasonChunkFraming)
	}
	if !d.params.signedChunks {
		return int64(size), "", nil
	}
	signature, ok := strings.CutPrefix(extension, chunkSignaturePrefix)
	if !hasExtension || !ok || !isLowerHex(signature, signatureHexLength) {
		return 0, "", newError(ReasonChunkFraming)
	}
	return int64(size), signature, nil
}

func (d *chunkDecoder) loadSignedChunk(size int64, signature string) error {
	d.chunk.Reset()
	if _, err := io.CopyN(&d.chunk, d.src, size); err != nil {
		return readFailure(err)
	}
	if err := d.expectCRLF(); err != nil {
		return err
	}
	digest := sha256.Sum256(d.chunk.Bytes())
	if err := d.verifyChunkSignature(hex.EncodeToString(digest[:]), signature); err != nil {
		return err
	}
	d.pending = d.chunk.Bytes()
	d.consumed += size
	return nil
}

func (d *chunkDecoder) verifyChunkSignature(dataDigest, provided string) error {
	stringToSign := strings.Join([]string{chunkStringToSignTag, d.amzDate, d.scope, d.previous, emptyPayloadSHA256, dataDigest}, "\n")
	if !signaturesEqual(hexSignature(d.params.signingKey, stringToSign), provided) {
		return newError(ReasonSignature)
	}
	d.previous = provided
	return nil
}

func (d *chunkDecoder) finish(finalSignature string) error {
	if d.params.signedChunks {
		if err := d.verifyChunkSignature(emptyPayloadSHA256, finalSignature); err != nil {
			return err
		}
	}
	if d.params.trailerName != "" {
		if err := d.readTrailer(); err != nil {
			return err
		}
	} else if err := d.expectCRLF(); err != nil {
		return err
	}
	if d.params.decodedLength >= 0 && d.consumed != d.params.decodedLength {
		return newError(ReasonIncompleteBody)
	}
	if err := d.expectEnd(); err != nil {
		return err
	}
	d.finished = true
	return nil
}

func (d *chunkDecoder) readTrailer() error {
	used := 0
	found := false
	signature := ""
	var canonical string
	for {
		line, err := d.readLine(maxTrailerBytes - used)
		if err != nil {
			return err
		}
		used += len(line) + 2
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return newError(ReasonChunkFraming)
		}
		name = strings.ToLower(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		switch {
		case name == trailerSignatureName && d.params.signedTrailer && signature == "":
			signature = value
		case name == d.params.trailerName && !found:
			found = true
			d.trailerValue = value
			canonical = name + ":" + value + "\n"
		default:
			return newError(ReasonChunkFraming)
		}
	}
	if !found || (d.params.signedTrailer && !isLowerHex(signature, signatureHexLength)) {
		return newError(ReasonChunkFraming)
	}
	if !d.params.signedTrailer {
		return nil
	}
	stringToSign := strings.Join([]string{trailerStringToSign, d.amzDate, d.scope, d.previous, hexSHA256(canonical)}, "\n")
	if !signaturesEqual(hexSignature(d.params.signingKey, stringToSign), signature) {
		return newError(ReasonSignature)
	}
	return nil
}

func (d *chunkDecoder) readLine(limit int) (string, error) {
	if limit < 0 {
		return "", newError(ReasonChunkFraming)
	}
	var line []byte
	for {
		b, err := d.src.ReadByte()
		if err != nil {
			return "", readFailure(err)
		}
		if b == '\n' {
			if len(line) == 0 || line[len(line)-1] != '\r' {
				return "", newError(ReasonChunkFraming)
			}
			return string(line[:len(line)-1]), nil
		}
		line = append(line, b)
		if len(line) > limit+1 {
			return "", newError(ReasonChunkFraming)
		}
	}
}

func (d *chunkDecoder) expectCRLF() error {
	var pair [2]byte
	if _, err := io.ReadFull(d.src, pair[:]); err != nil {
		return readFailure(err)
	}
	if pair[0] != '\r' || pair[1] != '\n' {
		return newError(ReasonChunkFraming)
	}
	return nil
}

func (d *chunkDecoder) expectEnd() error {
	_, err := d.src.ReadByte()
	switch {
	case err == nil:
		return newError(ReasonChunkFraming)
	case errors.Is(err, io.EOF):
		return nil
	default:
		return readFailure(err)
	}
}
