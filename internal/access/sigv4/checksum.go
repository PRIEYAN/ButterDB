package sigv4

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"strings"
)

const checksumHeaderPrefix = "x-amz-checksum-"

type checksumAlgorithm struct {
	name string
	size int
	new  func() hash.Hash
}

func checksumAlgorithms() []checksumAlgorithm {
	return []checksumAlgorithm{
		{name: "crc32", size: 4, new: func() hash.Hash { return crc32.NewIEEE() }},
		{name: "crc32c", size: 4, new: func() hash.Hash { return crc32.New(crc32.MakeTable(crc32.Castagnoli)) }},
		{name: "crc64nvme", size: 8, new: func() hash.Hash { return crc64.New(crc64.MakeTable(0x9a6c9329ac4bc9b5)) }},
		{name: "sha1", size: sha1.Size, new: sha1.New},
		{name: "sha256", size: sha256.Size, new: sha256.New},
	}
}

func checksumAlgorithmNamed(name string) (checksumAlgorithm, bool) {
	for _, algorithm := range checksumAlgorithms() {
		if algorithm.name == name {
			return algorithm, true
		}
	}
	return checksumAlgorithm{}, false
}

func checksumAlgorithmOfTrailer(trailer string) (checksumAlgorithm, bool) {
	name, ok := strings.CutPrefix(trailer, checksumHeaderPrefix)
	if !ok {
		return checksumAlgorithm{}, false
	}
	return checksumAlgorithmNamed(name)
}

type checksumVerifier struct {
	algorithm checksumAlgorithm
	hash      hash.Hash
	expected  []byte
}

func newChecksumVerifier(algorithm checksumAlgorithm, expectedBase64 string) (*checksumVerifier, error) {
	verifier := &checksumVerifier{algorithm: algorithm, hash: algorithm.new()}
	if expectedBase64 == "" {
		return verifier, nil
	}
	expected, err := decodeChecksum(algorithm, expectedBase64)
	if err != nil {
		return nil, err
	}
	verifier.expected = expected
	return verifier, nil
}

func decodeChecksum(algorithm checksumAlgorithm, valueBase64 string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(valueBase64)
	if err != nil || len(decoded) != algorithm.size {
		e := newError(ReasonChecksumMalformed)
		e.Algorithm = algorithm.name
		return nil, e
	}
	return decoded, nil
}

func (c *checksumVerifier) verify(trailerValue string, fromTrailer bool) (string, error) {
	expected := c.expected
	if fromTrailer {
		decoded, err := decodeChecksum(c.algorithm, trailerValue)
		if err != nil {
			return "", err
		}
		expected = decoded
	}
	computed := c.hash.Sum(nil)
	if string(computed) != string(expected) {
		e := newError(ReasonChecksumMismatch)
		e.Algorithm = c.algorithm.name
		return "", e
	}
	return base64.StdEncoding.EncodeToString(computed), nil
}
