package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
)

// EphemeralCredential holds all per-instance cryptographic material generated
// at provisioning time.
//
// Agent keypair: used for client authentication — runlease SSH-es in as
// runlease-agent using this key. The private half never leaves the controller.
//
// Host keypair: injected into the instance via user-data so sshd presents a
// controller-chosen key. The controller connects with FixedHostKey, so the
// host identity is established before the instance boots — no TOFU window.
type EphemeralCredential struct {
	PrivateKeyPEM []byte    `json:"privateKeyPEM"`
	PublicKeySSH  string    `json:"publicKeySSH"`
	Fingerprint   string    `json:"fingerprint"`

	HostPrivateKeyPEM []byte `json:"hostPrivateKeyPEM"`
	HostPublicKeySSH  string `json:"hostPublicKeySSH"`
	HostFingerprint   string `json:"hostFingerprint"`

	CreatedAt time.Time `json:"createdAt"`
}

// encryptedFile is the on-disk format for stored credentials.
type encryptedFile struct {
	V    int    `json:"v"`
	Data []byte `json:"data"` // AES-256-GCM(nonce ‖ ciphertext)
}

// Generate creates a fresh agent keypair and a fresh host keypair.
func Generate() (*EphemeralCredential, error) {
	agent, err := newKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generating agent keypair: %w", err)
	}
	host, err := newKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generating host keypair: %w", err)
	}
	return &EphemeralCredential{
		PrivateKeyPEM:     agent.privatePEM,
		PublicKeySSH:      agent.publicSSH,
		Fingerprint:       agent.fingerprint,
		HostPrivateKeyPEM: host.privatePEM,
		HostPublicKeySSH:  host.publicSSH,
		HostFingerprint:   host.fingerprint,
		CreatedAt:         time.Now().UTC(),
	}, nil
}

type keyPair struct {
	privatePEM  []byte
	publicSSH   string
	fingerprint string
}

func newKeyPair() (*keyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	// OpenSSH private key format ("-----BEGIN OPENSSH PRIVATE KEY-----") is
	// required by sshd for host keys; PKCS#8 is not recognised.
	privBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, err
	}
	privPEM := pem.EncodeToMemory(privBlock)
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return &keyPair{
		privatePEM:  privPEM,
		publicSSH:   string(ssh.MarshalAuthorizedKey(sshPub)),
		fingerprint: ssh.FingerprintSHA256(sshPub),
	}, nil
}

// Save encrypts cred with key (AES-256-GCM) and writes it to dir/<instanceID>.json.
func Save(dir, instanceID string, cred *EphemeralCredential, key []byte) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating credential dir: %w", err)
	}
	plain, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("marshaling credential: %w", err)
	}
	ciphertext, err := encrypt(key, plain)
	if err != nil {
		return fmt.Errorf("encrypting credential: %w", err)
	}
	out, err := json.Marshal(encryptedFile{V: 1, Data: ciphertext})
	if err != nil {
		return fmt.Errorf("marshaling encrypted file: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, instanceID+".json"), out, 0600); err != nil {
		return fmt.Errorf("writing credential: %w", err)
	}
	return nil
}

// Load decrypts and returns the credential stored for instanceID in dir.
func Load(dir, instanceID string, key []byte) (*EphemeralCredential, error) {
	raw, err := os.ReadFile(filepath.Join(dir, instanceID+".json"))
	if err != nil {
		return nil, fmt.Errorf("reading credential: %w", err)
	}
	var ef encryptedFile
	if err := json.Unmarshal(raw, &ef); err != nil {
		return nil, fmt.Errorf("parsing credential file: %w", err)
	}
	plain, err := decrypt(key, ef.Data)
	if err != nil {
		return nil, fmt.Errorf("decrypting credential: %w", err)
	}
	var cred EphemeralCredential
	if err := json.Unmarshal(plain, &cred); err != nil {
		return nil, fmt.Errorf("parsing credential: %w", err)
	}
	return &cred, nil
}

func Delete(dir, instanceID string) error {
	if err := os.Remove(filepath.Join(dir, instanceID+".json")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting credential: %w", err)
	}
	return nil
}

// LoadOrCreateMasterKey returns the 32-byte AES key at path, generating and
// persisting it (mode 0600) if it does not yet exist.
func LoadOrCreateMasterKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != 32 {
			return nil, fmt.Errorf("master key at %s has unexpected length %d (want 32)", path, len(data))
		}
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading master key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generating master key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("creating key dir: %w", err)
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil, fmt.Errorf("writing master key: %w", err)
	}
	return key, nil
}

func encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed (wrong key?): %w", err)
	}
	return plain, nil
}
