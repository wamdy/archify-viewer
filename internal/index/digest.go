package index

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"time"
)

// nowUnix 留出注入点，保证索引结果可在测试里断言。
var nowUnix = func() int64 { return time.Now().Unix() }

// idFor 是对外唯一寻址键：产物 HTML 绝对路径的 sha256 前 12 位。
// 同一台机器同一件产物重启后 ID 不变，深链因此可长期粘贴。
func idFor(htmlPath string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(htmlPath)))
	return hex.EncodeToString(sum[:])[:12]
}

// SHA256File 供只读不变性断言（红线 1）与 list --verbose 排障使用。
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
