package gesso

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Elagoht/gesso/validate"
)

// Language translates the answers of a request whose Accept-Language asks for
// it: the field errors' messages, and problems' details by their code.
// Config.Languages names each by its primary tag ("tr"); English needs none.
//
//	tr := gesso.Turkish
//	tr.Details = maps.Clone(tr.Details)
//	tr.Details["note_locked"] = "not kilitli"
//	gesso.New(gesso.Config{Languages: map[string]gesso.Language{"tr": tr}}, …)
type Language struct {
	// Fields is a field error's message, or "" to keep the English one.
	Fields func(validate.FieldError) string
	// Details are problem details by code; a code not here keeps its own.
	Details map[string]string
}

// Turkish translates gesso's own codes and rules.
var Turkish = Language{
	Fields: validate.Turkish,
	Details: map[string]string{
		"not_found":              "kaynak bulunamadı",
		"method_not_allowed":     "bu yöntem burada kullanılamaz",
		"invalid_json":           "gövde geçerli bir JSON değil",
		"invalid_input":          "istek doğrulamadan geçemedi",
		"unsupported_media_type": "gövdenin türü desteklenmiyor",
		"body_too_large":         "gövde çok büyük",
		"timeout":                "istek çok uzun sürdü",
		"internal":               "bir şeyler ters gitti",
		"unavailable":            "servis şu an hizmet veremiyor",
		"too_many_requests":      "çok fazla istek: biraz sonra tekrar deneyin",
		"bad_gateway":            "bu servisin çağırdığı bir servis başarısız oldu",
		"unauthenticated":        "geçerli bir kimlik bilgisi gerekiyor",
		"invalid_token":          "kimlik bilgisi bozuk, süresi dolmuş ya da iptal edilmiş",
		"forbidden":              "kimlik bilgisi buna izin vermiyor",
		"invalid_form":           "gövde geçerli bir form değil",
		"invalid_cursor":         "imleç bu API'nin verdiği bir imleç değil",
		"precondition_failed":    "kaynak okunduğundan beri değişti: yeniden okuyun",
	},
}

// language is the Language r's Accept-Language prefers, by quality, among
// those configured, and its tag.
func language(r *http.Request, langs map[string]Language) (string, Language, bool) {
	if len(langs) == 0 {
		return "", Language{}, false
	}
	type choice struct {
		tag string
		q   float64
	}
	var choices []choice
	for part := range strings.SplitSeq(r.Header.Get("Accept-Language"), ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		primary, _, _ := strings.Cut(strings.ToLower(tag), "-")
		if primary != "" && q > 0 {
			choices = append(choices, choice{primary, q})
		}
	}
	slices.SortStableFunc(choices, func(a, b choice) int {
		switch {
		case a.q > b.q:
			return -1
		case a.q < b.q:
			return 1
		}
		return 0
	})
	for _, c := range choices {
		if c.tag == "en" {
			return "", Language{}, false // English is what gesso speaks
		}
		if l, ok := langs[c.tag]; ok {
			return c.tag, l, true
		}
	}
	return "", Language{}, false
}

// translate is p in r's language, if one is configured.
func translate(r *http.Request, langs map[string]Language, p Problem, h http.Header) Problem {
	if len(langs) > 0 {
		h.Add("Vary", "Accept-Language")
	}
	tag, l, ok := language(r, langs)
	if !ok {
		return p
	}
	if d, ok := l.Details[p.Code]; ok {
		p.Detail = d
	}
	if l.Fields != nil && len(p.Errors) > 0 {
		fields := slices.Clone(p.Errors)
		for i, fe := range fields {
			if msg := l.Fields(fe); msg != "" {
				fields[i].Message = msg
			}
		}
		p.Errors = fields
	}
	h.Set("Content-Language", tag)
	return p
}
