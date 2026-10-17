package crypto

import("crypto/aes";"crypto/cipher";"crypto/rand";"crypto/sha256";"encoding/base64";"errors";"io")

func HashToken(s string) string { h:=sha256.Sum256([]byte(s)); return base64.RawURLEncoding.EncodeToString(h[:]) }
func Encrypt(key,plain []byte)([]byte,error){ if len(key)!=32{return nil,errors.New("encryption key must be exactly 32 bytes")}; b,e:=aes.NewCipher(key);if e!=nil{return nil,e};g,e:=cipher.NewGCM(b);if e!=nil{return nil,e};n:=make([]byte,g.NonceSize());if _,e=io.ReadFull(rand.Reader,n);e!=nil{return nil,e};return g.Seal(n,n,plain,nil),nil }
func Decrypt(key,ciphertext []byte)([]byte,error){if len(key)!=32{return nil,errors.New("encryption key must be exactly 32 bytes")};b,e:=aes.NewCipher(key);if e!=nil{return nil,e};g,e:=cipher.NewGCM(b);if e!=nil{return nil,e};n:=g.NonceSize();if len(ciphertext)<n{return nil,errors.New("ciphertext too short")};return g.Open(nil,ciphertext[:n],ciphertext[n:],nil)}
