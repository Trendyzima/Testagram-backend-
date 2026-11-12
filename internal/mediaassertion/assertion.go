package mediaassertion

import (
 "crypto/hmac"
 "crypto/rand"
 "crypto/sha256"
 "encoding/base64"
 "encoding/hex"
 "encoding/json"
 "errors"
 "fmt"
 "regexp"
 "strings"
 "time"
)

const (issuer="testagram-backend"; audience="testagram-cdn"; version="v1"; DefaultTTL=2*time.Minute; maxTTL=5*time.Minute)
var (userPattern=regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`); keyPattern=regexp.MustCompile(`^users/([A-Za-z0-9-]{1,128})/([a-f0-9]{32})$`))
type Claims struct { Version string `json:"v"`; Issuer string `json:"iss"`; Audience string `json:"aud"`; Subject string `json:"sub"`; Key string `json:"key"`; Visibility string `json:"visibility"`; IssuedAt int64 `json:"iat"`; ExpiresAt int64 `json:"exp"`; JTI string `json:"jti"` }
type Issuer struct { secret []byte; ttl time.Duration }
func New(secret []byte, ttl time.Duration) (*Issuer,error) { if len(secret)<32{return nil,errors.New("CDN assertion secret must contain at least 32 bytes")};if ttl==0{ttl=DefaultTTL};if ttl<15*time.Second||ttl>maxTTL{return nil,errors.New("CDN assertion TTL must be between 15 seconds and 5 minutes")};return &Issuer{secret:append([]byte(nil),secret...),ttl:ttl},nil }
func(i *Issuer) Issue(key,viewer string,now time.Time)(string,time.Time,error){if i==nil{return "",time.Time{},errors.New("CDN assertion issuer is unavailable")};m:=keyPattern.FindStringSubmatch(key);if !userPattern.MatchString(viewer)||len(m)!=3||m[1]!=viewer{return "",time.Time{},errors.New("media key is not owned by the asserted viewer")};id:=make([]byte,16);if _,e:=rand.Read(id);e!=nil{return "",time.Time{},fmt.Errorf("generate assertion id: %w",e)};now=now.UTC().Truncate(time.Second);exp:=now.Add(i.ttl);c:=Claims{version,issuer,audience,viewer,key,"private",now.Unix(),exp.Unix(),hex.EncodeToString(id)};b,e:=json.Marshal(c);if e!=nil{return "",time.Time{},e};p:=base64.RawURLEncoding.EncodeToString(b);input:=version+"."+p;mac:=hmac.New(sha256.New,i.secret);_,_=mac.Write([]byte(input));return input+"."+base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),exp,nil}
func Verify(secret []byte,token,key,viewer string,now time.Time)(Claims,error){var c Claims;if len(secret)<32||len(token)>4096{return c,errors.New("invalid CDN assertion")};p:=strings.Split(token,".");if len(p)!=3||p[0]!=version{return c,errors.New("invalid CDN assertion format")};sig,e:=base64.RawURLEncoding.DecodeString(p[2]);if e!=nil||len(sig)!=sha256.Size{return c,errors.New("invalid CDN assertion signature")};mac:=hmac.New(sha256.New,secret);_,_=mac.Write([]byte(p[0]+"."+p[1]));if !hmac.Equal(sig,mac.Sum(nil)){return c,errors.New("invalid CDN assertion signature")};b,e:=base64.RawURLEncoding.DecodeString(p[1]);if e!=nil||len(b)>2048||json.Unmarshal(b,&c)!=nil{return c,errors.New("invalid CDN assertion payload")};now=now.UTC();if c.Version!=version||c.Issuer!=issuer||c.Audience!=audience||c.Visibility!="private"||c.Subject!=viewer||c.Key!=key||!userPattern.MatchString(c.Subject)||!keyPattern.MatchString(c.Key)||!strings.HasPrefix(c.Key,"users/"+c.Subject+"/")||c.IssuedAt<=0||c.ExpiresAt<=c.IssuedAt||c.ExpiresAt-c.IssuedAt>int64(maxTTL/time.Second)||c.IssuedAt>now.Add(30*time.Second).Unix()||c.ExpiresAt<=now.Unix()||len(c.JTI)!=32{return Claims{},errors.New("CDN assertion claims are invalid or expired")};if _,e:=hex.DecodeString(c.JTI);e!=nil{return Claims{},errors.New("CDN assertion id is invalid")};return c,nil}

func (i *Issuer) VerifyForKey(token, key string, now time.Time) (Claims, error) {
	if i == nil { return Claims{}, errors.New("CDN assertion issuer is unavailable") }
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != version { return Claims{}, errors.New("invalid CDN assertion format") }
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > 2048 { return Claims{}, errors.New("invalid CDN assertion payload") }
	var untrusted Claims
	if err := json.Unmarshal(payload, &untrusted); err != nil { return Claims{}, errors.New("invalid CDN assertion payload") }
	return Verify(i.secret, token, key, untrusted.Subject, now)
}
