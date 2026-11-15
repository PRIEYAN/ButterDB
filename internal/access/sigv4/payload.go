package sigv4

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"strings"
)

var _ io.ReadCloser = (*PayloadReader)(nil)

type PayloadReader struct {
	closer        io.Closer
	source        io.Reader
	decoder       *chunkDecoder
	payloadHash   hash.Hash
	wantSHA256    string
	checksum      *checksumVerifier
	checksumValue string
	err           error
}

func (v *Verifier) Body(r *http.Request, a Auth) (*PayloadReader, error) {
	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	reader := &PayloadReader{closer: body, source: body}
	if err := reader.configureMode(r, a); err != nil {
		return nil, err
	}
	if err := reader.configureChecksum(r, a); err != nil {
		return nil, err
	}
	return reader, nil
}

func (p *PayloadReader) configureMode(r *http.Request, a Auth) error {
	switch a.Mode {
	case PayloadNone, PayloadUnsigned:
		return nil
	case PayloadSignedHex:
		if bodylessMethod(r.Method) {
			p.source = http.NoBody
			return nil
		}
		p.payloadHash = sha256.New()
		p.wantSHA256 = a.ContentSHA256
		return nil
	case PayloadStreamingSigned, PayloadStreamingSignedTrailer, PayloadStreamingUnsignedTrailer:
		return p.configureStreaming(a)
	default:
		return newError(ReasonBadContentSHA256)
	}
}

func (p *PayloadReader) configureStreaming(a Auth) error {
	signed := a.Mode != PayloadStreamingUnsignedTrailer
	if a.DecodedLength < 0 || (signed && (len(a.signingKey) == 0 || a.seed == "")) {
		return newError(ReasonChunkFraming)
	}
	p.decoder = newChunkDecoder(p.source, chunkParams{
		signedChunks:  signed,
		signedTrailer: a.Mode == PayloadStreamingSignedTrailer,
		trailerName:   a.Trailer,
		decodedLength: a.DecodedLength,
		signingKey:    a.signingKey,
		seed:          a.seed,
		requestTime:   a.RequestTime,
		region:        a.Region,
	})
	p.source = p.decoder
	return nil
}

func (p *PayloadReader) configureChecksum(r *http.Request, a Auth) error {
	headerAlgorithm, headerValue, err := checksumHeader(r)
	if err != nil {
		return err
	}
	if a.Trailer != "" {
		if headerValue != "" {
			return newError(ReasonChecksumMalformed)
		}
		algorithm, ok := checksumAlgorithmOfTrailer(a.Trailer)
		if !ok {
			return newError(ReasonChecksumMalformed)
		}
		p.checksum, err = newChecksumVerifier(algorithm, "")
		return err
	}
	if headerValue == "" {
		return nil
	}
	p.checksum, err = newChecksumVerifier(headerAlgorithm, headerValue)
	return err
}

func checksumHeader(r *http.Request) (checksumAlgorithm, string, error) {
	var found checksumAlgorithm
	value := ""
	count := 0
	for name, values := range r.Header {
		algorithm, ok := checksumAlgorithmOfTrailer(strings.ToLower(name))
		if !ok {
			continue
		}
		count++
		if len(values) != 1 || values[0] == "" {
			return checksumAlgorithm{}, "", newError(ReasonChecksumMalformed)
		}
		found, value = algorithm, values[0]
	}
	if count > 1 {
		return checksumAlgorithm{}, "", newError(ReasonChecksumMalformed)
	}
	return found, value, nil
}

func (p *PayloadReader) Read(b []byte) (int, error) {
	if p.err != nil {
		return 0, p.err
	}
	n, err := p.source.Read(b)
	if n > 0 {
		p.observe(b[:n])
	}
	switch {
	case err == nil:
		return n, nil
	case errors.Is(err, io.EOF):
		if verifyErr := p.verifyAtEOF(); verifyErr != nil {
			p.err = verifyErr
			return n, verifyErr
		}
		p.err = io.EOF
		return n, io.EOF
	default:
		p.err = classifyReadError(err)
		return n, p.err
	}
}

func classifyReadError(err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	return readFailure(err)
}

func (p *PayloadReader) observe(data []byte) {
	if p.payloadHash != nil {
		p.payloadHash.Write(data)
	}
	if p.checksum != nil {
		p.checksum.hash.Write(data)
	}
}

func (p *PayloadReader) verifyAtEOF() error {
	if p.payloadHash != nil {
		got := hex.EncodeToString(p.payloadHash.Sum(nil))
		if !signaturesEqual(got, p.wantSHA256) {
			e := newError(ReasonContentSHA256Mismatch)
			e.ClientSHA256, e.ServerSHA256 = p.wantSHA256, got
			return e
		}
	}
	if p.checksum == nil {
		return nil
	}
	trailer := ""
	fromTrailer := p.decoder != nil && p.decoder.params.trailerName != ""
	if fromTrailer {
		trailer = p.decoder.trailerValue
	}
	value, err := p.checksum.verify(trailer, fromTrailer)
	if err != nil {
		return err
	}
	p.checksumValue = value
	return nil
}

func (p *PayloadReader) Close() error {
	if err := p.closer.Close(); err != nil {
		return fmt.Errorf("sigv4: close body: %w", err)
	}
	return nil
}

func (p *PayloadReader) Checksum() (algorithm string, value string) {
	if p.checksum == nil || p.checksumValue == "" {
		return "", ""
	}
	return p.checksum.algorithm.name, p.checksumValue
}
