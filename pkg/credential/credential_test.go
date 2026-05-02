package credential

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func TestGenerate_agentKeypair(t *testing.T) {
	cred, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(cred.PrivateKeyPEM) == 0 {
		t.Error("PrivateKeyPEM is empty")
	}
	signer, err := ssh.ParsePrivateKey(cred.PrivateKeyPEM)
	if err != nil {
		t.Fatalf("ParsePrivateKey: %v", err)
	}
	if got := ssh.FingerprintSHA256(signer.PublicKey()); got != cred.Fingerprint {
		t.Errorf("Fingerprint mismatch: got %q, want %q", got, cred.Fingerprint)
	}
}

func TestGenerate_hostKeypair(t *testing.T) {
	cred, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(cred.HostPrivateKeyPEM) == 0 {
		t.Error("HostPrivateKeyPEM is empty")
	}
	if cred.HostPublicKeySSH == "" {
		t.Error("HostPublicKeySSH is empty")
	}
	if cred.HostFingerprint == "" {
		t.Error("HostFingerprint is empty")
	}
	signer, err := ssh.ParsePrivateKey(cred.HostPrivateKeyPEM)
	if err != nil {
		t.Fatalf("ParsePrivateKey (host): %v", err)
	}
	if got := ssh.FingerprintSHA256(signer.PublicKey()); got != cred.HostFingerprint {
		t.Errorf("HostFingerprint mismatch: got %q, want %q", got, cred.HostFingerprint)
	}
}

func TestGenerate_agentAndHostAreDistinct(t *testing.T) {
	cred, _ := Generate()
	if cred.Fingerprint == cred.HostFingerprint {
		t.Error("agent and host keypairs should be different")
	}
}

func TestGenerate_isUnique(t *testing.T) {
	a, _ := Generate()
	b, _ := Generate()
	if a.Fingerprint == b.Fingerprint {
		t.Error("two Generate() calls produced the same agent key")
	}
	if a.HostFingerprint == b.HostFingerprint {
		t.Error("two Generate() calls produced the same host key")
	}
}

func TestSaveLoad_encrypted(t *testing.T) {
	dir := t.TempDir()
	key := testKey(t)
	cred, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := Save(dir, "i-abc123", cred, key); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "i-abc123.json"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("file perm = %o, want 0600", perm)
	}

	got, err := Load(dir, "i-abc123", key)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Fingerprint != cred.Fingerprint {
		t.Errorf("Fingerprint mismatch")
	}
	if got.HostFingerprint != cred.HostFingerprint {
		t.Errorf("HostFingerprint mismatch")
	}
	if got.HostPublicKeySSH != cred.HostPublicKeySSH {
		t.Error("HostPublicKeySSH mismatch")
	}
}

func TestLoad_wrongKey(t *testing.T) {
	dir := t.TempDir()
	key := testKey(t)
	cred, _ := Generate()
	_ = Save(dir, "i-abc123", cred, key)

	_, err := Load(dir, "i-abc123", make([]byte, 32))
	if err == nil {
		t.Fatal("expected error with wrong key, got nil")
	}
}

func TestLoad_notFound(t *testing.T) {
	_, err := Load(t.TempDir(), "nonexistent", testKey(t))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	cred, _ := Generate()
	_ = Save(dir, "i-del", cred, testKey(t))

	if err := Delete(dir, "i-del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "i-del.json")); !os.IsNotExist(err) {
		t.Error("file still exists after Delete")
	}
	if err := Delete(dir, "i-del"); err != nil {
		t.Errorf("second Delete (idempotent): %v", err)
	}
}

func TestLoadOrCreateMasterKey_createsOnFirstCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	key, err := LoadOrCreateMasterKey(path)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("key length = %d, want 32", len(key))
	}
	if perm := func() os.FileMode { i, _ := os.Stat(path); return i.Mode().Perm() }(); perm != 0600 {
		t.Errorf("key file perm = %o, want 0600", perm)
	}
}

func TestLoadOrCreateMasterKey_stable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	k1, _ := LoadOrCreateMasterKey(path)
	k2, _ := LoadOrCreateMasterKey(path)
	for i := range k1 {
		if k1[i] != k2[i] {
			t.Fatal("key changed between calls")
		}
	}
}
