package sigv4

import (
	"container/list"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

const (
	signingAlgorithm   = "AWS4-HMAC-SHA256"
	scopeService       = "s3"
	scopeTerminator    = "aws4_request"
	timeLayout         = "20060102T150405Z"
	dateLayout         = "20060102"
	emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	signatureHexLength = 64
	defaultRegion      = "us-east-1"
	defaultMaxSkew     = 15 * time.Minute
	defaultMaxPresign  = 7 * 24 * time.Hour
	signingKeyCacheMax = 1024
	signingKeyCacheTTL = 24 * time.Hour
)

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func deriveSigningKey(secret []byte, date, region string) []byte {
	seed := make([]byte, 0, len(secret)+4)
	seed = append(seed, "AWS4"...)
	seed = append(seed, secret...)
	dateKey := hmacSHA256(seed, date)
	clear(seed)
	regionKey := hmacSHA256(dateKey, region)
	serviceKey := hmacSHA256(regionKey, scopeService)
	return hmacSHA256(serviceKey, scopeTerminator)
}

func credentialScopeText(date, region string) string {
	return date + "/" + region + "/" + scopeService + "/" + scopeTerminator
}

func hexSHA256(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func requestStringToSign(requestTime time.Time, scope, canonicalRequest string) string {
	return signingAlgorithm + "\n" + requestTime.UTC().Format(timeLayout) + "\n" + scope + "\n" + hexSHA256(canonicalRequest)
}

func hexSignature(signingKey []byte, stringToSign string) string {
	return hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
}

func signaturesEqual(computed, provided string) bool {
	return hmac.Equal([]byte(computed), []byte(provided))
}

func isLowerHex(text string, length int) bool {
	if len(text) != length {
		return false
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

type signingKeyID struct {
	accessKeyID string
	date        string
	region      string
}

type signingKeyEntry struct {
	id          signingKeyID
	fingerprint [sha256.Size]byte
	signingKey  []byte
	expiresAt   time.Time
}

type signingKeyCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[signingKeyID]*list.Element
	recency *list.List
}

func newSigningKeyCache(now func() time.Time) *signingKeyCache {
	return &signingKeyCache{
		now:     now,
		entries: make(map[signingKeyID]*list.Element),
		recency: list.New(),
	}
}

func (c *signingKeyCache) signingKey(id signingKeyID, secret []byte) []byte {
	fingerprint := sha256.Sum256(secret)
	now := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()

	if element, ok := c.entries[id]; ok {
		entry := element.Value.(*signingKeyEntry)
		if now.Before(entry.expiresAt) && hmac.Equal(entry.fingerprint[:], fingerprint[:]) {
			c.recency.MoveToFront(element)
			return entry.signingKey
		}
		c.recency.Remove(element)
		delete(c.entries, id)
	}

	entry := &signingKeyEntry{
		id:          id,
		fingerprint: fingerprint,
		signingKey:  deriveSigningKey(secret, id.date, id.region),
		expiresAt:   now.Add(signingKeyCacheTTL),
	}
	c.entries[id] = c.recency.PushFront(entry)
	for c.recency.Len() > signingKeyCacheMax {
		oldest := c.recency.Back()
		c.recency.Remove(oldest)
		delete(c.entries, oldest.Value.(*signingKeyEntry).id)
	}
	return entry.signingKey
}

func (c *signingKeyCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recency.Len()
}
