package auth

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const InitFixedSize = 1 + 32 + 32 + 16
const ChallengeSize = 32 + 32 + 16 + ed25519.SignatureSize

type Init struct {
	Name      string
	PublicKey [32]byte
	Ephemeral [32]byte
	Nonce     [16]byte
}

type Challenge struct {
	ServerKey [32]byte
	Ephemeral [32]byte
	Nonce     [16]byte
	Signature [64]byte
}

func NewInit(name string, private ed25519.PrivateKey) (Init, *ecdh.PrivateKey, error) {
	var init Init
	if len(name) == 0 || len(name) > 64 || len(private) != ed25519.PrivateKeySize {
		return init, nil, errors.New("invalid authentication identity")
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return init, nil, err
	}
	init.Name = name
	copy(init.PublicKey[:], private.Public().(ed25519.PublicKey))
	copy(init.Ephemeral[:], ephemeral.PublicKey().Bytes())
	if _, err := rand.Read(init.Nonce[:]); err != nil {
		return Init{}, nil, err
	}
	return init, ephemeral, nil
}

func (i Init) Encode() []byte {
	payload := make([]byte, 0, InitFixedSize+len(i.Name))
	payload = append(payload, byte(len(i.Name)))
	payload = append(payload, i.PublicKey[:]...)
	payload = append(payload, i.Ephemeral[:]...)
	payload = append(payload, i.Nonce[:]...)
	return append(payload, i.Name...)
}

func DecodeInit(payload []byte) (Init, error) {
	var init Init
	if len(payload) < InitFixedSize || len(payload) != InitFixedSize+int(payload[0]) || payload[0] == 0 || payload[0] > 64 {
		return init, errors.New("invalid authentication init")
	}
	init.Name = string(payload[81:])
	copy(init.PublicKey[:], payload[1:33])
	copy(init.Ephemeral[:], payload[33:65])
	copy(init.Nonce[:], payload[65:81])
	if init.PublicKey == ([32]byte{}) || init.Ephemeral == ([32]byte{}) {
		return Init{}, errors.New("empty authentication key")
	}
	return init, nil
}

func NewChallenge(init Init, signer ed25519.PrivateKey, requestID, version uint32) (Challenge, *ecdh.PrivateKey, error) {
	var challenge Challenge
	if len(signer) != ed25519.PrivateKeySize {
		return challenge, nil, errors.New("invalid server signing key")
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return challenge, nil, err
	}
	copy(challenge.ServerKey[:], signer.Public().(ed25519.PublicKey))
	copy(challenge.Ephemeral[:], ephemeral.PublicKey().Bytes())
	if _, err := rand.Read(challenge.Nonce[:]); err != nil {
		return Challenge{}, nil, err
	}
	digest := Transcript(init, challenge, requestID, version)
	copy(challenge.Signature[:], ed25519.Sign(signer, serverMessage(digest)))
	return challenge, ephemeral, nil
}

func (c Challenge) Encode() []byte {
	payload := make([]byte, 0, ChallengeSize)
	payload = append(payload, c.ServerKey[:]...)
	payload = append(payload, c.Ephemeral[:]...)
	payload = append(payload, c.Nonce[:]...)
	return append(payload, c.Signature[:]...)
}

func DecodeChallenge(payload []byte) (Challenge, error) {
	var challenge Challenge
	if len(payload) != ChallengeSize {
		return challenge, errors.New("invalid authentication challenge")
	}
	copy(challenge.ServerKey[:], payload[:32])
	copy(challenge.Ephemeral[:], payload[32:64])
	copy(challenge.Nonce[:], payload[64:80])
	copy(challenge.Signature[:], payload[80:])
	return challenge, nil
}

func Transcript(init Init, challenge Challenge, requestID, version uint32) [32]byte {
	hash := sha256.New()
	hash.Write([]byte("sonoryx-auth-v1"))
	var numbers [8]byte
	binary.BigEndian.PutUint32(numbers[:4], requestID)
	binary.BigEndian.PutUint32(numbers[4:], version)
	hash.Write(numbers[:])
	hash.Write(init.Encode())
	hash.Write(challenge.ServerKey[:])
	hash.Write(challenge.Ephemeral[:])
	hash.Write(challenge.Nonce[:])
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func serverMessage(digest [32]byte) []byte {
	return append([]byte("sonoryx-server-auth-v1"), digest[:]...)
}
func clientMessage(digest [32]byte) []byte {
	return append([]byte("sonoryx-client-auth-v1"), digest[:]...)
}

func VerifyChallenge(init Init, challenge Challenge, requestID, version uint32) error {
	digest := Transcript(init, challenge, requestID, version)
	if !ed25519.Verify(challenge.ServerKey[:], serverMessage(digest), challenge.Signature[:]) {
		return errors.New("invalid server authentication signature")
	}
	return nil
}

func SignFinish(private ed25519.PrivateKey, digest [32]byte) []byte {
	return ed25519.Sign(private, clientMessage(digest))
}

func VerifyFinish(publicKey [32]byte, digest [32]byte, signature []byte) error {
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey[:], clientMessage(digest), signature) {
		return errors.New("invalid client authentication signature")
	}
	return nil
}

func DeriveKeys(ephemeral *ecdh.PrivateKey, peer [32]byte, digest [32]byte) (clientToServer, serverToClient, acknowledge [32]byte, err error) {
	publicKey, err := ecdh.X25519().NewPublicKey(peer[:])
	if err != nil {
		return clientToServer, serverToClient, acknowledge, err
	}
	shared, err := ephemeral.ECDH(publicKey)
	if err != nil {
		return clientToServer, serverToClient, acknowledge, err
	}
	reader := hkdf.New(sha256.New, shared, digest[:], []byte("sonoryx-udp-v1 traffic and ack"))
	for _, key := range []*[32]byte{&clientToServer, &serverToClient, &acknowledge} {
		if _, err := io.ReadFull(reader, key[:]); err != nil {
			return clientToServer, serverToClient, acknowledge, fmt.Errorf("derive session key: %w", err)
		}
	}
	return clientToServer, serverToClient, acknowledge, nil
}

func AckMAC(key [32]byte, digest [32]byte, sessionID uint64, joinLevel uint16, permissions uint8) [32]byte {
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte("sonoryx-auth-ack-v1"))
	mac.Write(digest[:])
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], sessionID)
	mac.Write(id[:])
	var privileges [3]byte
	binary.BigEndian.PutUint16(privileges[:2], joinLevel)
	privileges[2] = permissions
	mac.Write(privileges[:])
	var result [32]byte
	copy(result[:], mac.Sum(nil))
	return result
}
