# Files, cache and mail

## Files

```go
files, err := s3.New(s3.Options{
	Endpoint: "https://s3.eu-central-1.amazonaws.com", Region: "eu-central-1",
	Bucket: "uploads", AccessKey: os.Getenv("S3_ACCESS_KEY"), SecretKey: os.Getenv("S3_SECRET_KEY"),
})
obj, err := files.Put(ctx, "avatars/42.png", upload)
```

`storage/s3` speaks to AWS S3 and what speaks its API — MinIO, Cloudflare R2,
Backblaze B2 — with `VirtualHost` for AWS's bucket-in-the-host addressing.
`storage.NewDisk(dir)` keeps files on a disk, `storage.NewMemory` in a test.
Keys are your own ids, never a client's file name.

## Cache

```go
c, err := redis.New(redis.Options{Addr: "localhost:6379", Password: os.Getenv("REDIS_PASSWORD"), Prefix: "myapp:"})
note, err := cache.GetOrLoad(ctx, c, "note:"+id, 5*time.Minute, load)
```

`cache/redis` is one cache every replica shares; `cache.NewMemory` one per
process. A cache that fails is passed over: `GetOrLoad` loads instead.

## Mail

```go
m := &mail.SMTP{Addr: "smtp.example.com:587", Username: "app", Password: os.Getenv("SMTP_PASSWORD"),
	From: "Uygulama <app@example.com>"}
err := m.Send(ctx, mail.Message{To: []string{user.Email}, Subject: "Şifre sıfırlama", Text: text, HTML: html})
```

STARTTLS is required unless `Security: mail.ImplicitTLS` (port 465) or, for a
mail catcher on this machine, `mail.Plain`. Send mail from a queue job, not the
request: a slow server should not slow the answer, and a failed send is retried.
`mail.Capture` keeps what a test sends.
