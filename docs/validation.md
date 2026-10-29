# Validation

Rules go in a `validate` tag, with go-playground/validator's names:

```go
type ReqSignup struct {
	Email    string   `json:"email" validate:"required,email"`
	Username string   `json:"username" validate:"required,min=3,max=20,alphanum,lowercase"`
	Age      int      `json:"age" validate:"gte=13"`
	Slug     string   `json:"slug" validate:"omitempty,slug"`
	Tags     []string `json:"tags" validate:"max=5,unique,dive,required,max=20"`
}
```

| Rule | Means |
| --- | --- |
| `required` | not the zero value (a pointer not nil) |
| `omitempty` | a zero value skips the rest |
| `min` `max` `len` `gt` `gte` `lt` `lte` | characters of a string, elements of a collection, a number's value |
| `eq` `ne` | a string's or number's value, a collection's length |
| `oneof=a b c` | one of the words |
| `email` `url` `uuid` `ip` `ipv4` `ipv6` `hostname` | the format |
| `alpha` `alphanum` `numeric` `lowercase` `uppercase` | the characters |
| `contains=x` `excludes=x` `startswith=x` `endswith=x` | the text |
| `slug` | `getting-started`: lower-case words joined by single hyphens |
| `unique` | no element twice |
| `dive` | the rules after it apply to each element |
| `notnull` | a `gesso.Optional` that is given is not null |

A failure answers 400 `invalid_input` with each field's error, named as the
client spells it: `tags[2]`, `address.city`. Every rule is also written into the
OpenAPI schema. A tag gesso does not know fails when the route is registered,
not when a request comes.

What tags cannot say, a `Validate` method does; it runs after the tags pass:

```go
func (in ReqSignup) Validate(ctx context.Context) []validate.FieldError {
	if in.Username == "admin" {
		return []validate.FieldError{{Field: "username", Code: "reserved", Message: "is reserved"}}
	}
	return nil
}
```

## In the client's language

```go
gesso.New(gesso.Config{Languages: map[string]gesso.Language{"tr": gesso.Turkish}})
```

A request with `Accept-Language: tr` gets its field errors and problem details
in Turkish ("en az 3 karakter olmalıdır"), `Content-Language: tr` saying so.
Add your own codes to a copy of `gesso.Turkish.Details`; a code without a
translation keeps its English.
