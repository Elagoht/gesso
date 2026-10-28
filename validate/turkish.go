package validate

import "strings"

// Turkish is each rule's message in Turkish, for gesso's Languages: "" for a
// code it does not know — an application's own — which keeps its message.
func Turkish(e FieldError) string {
	p := func(i int) string {
		if i < len(e.Params) {
			return e.Params[i]
		}
		return ""
	}
	size := func(chars, items, value string) string {
		switch e.Unit() {
		case "characters":
			return p(0) + " " + chars
		case "items":
			return p(0) + " " + items
		}
		return p(0) + value
	}
	switch e.Code {
	case "required":
		return "zorunludur"
	case "notnull":
		return "boş (null) olamaz"
	case "min", "gte":
		return "en az " + size("karakter olmalıdır", "öğe içermelidir", " olmalıdır")
	case "max", "lte":
		return "en fazla " + size("karakter olmalıdır", "öğe içermelidir", " olmalıdır")
	case "len":
		return "tam " + size("karakter olmalıdır", "öğe içermelidir", " olmalıdır")
	case "gt":
		return size("karakterden uzun olmalıdır", "öğeden fazla içermelidir", " değerinden büyük olmalıdır")
	case "lt":
		return size("karakterden kısa olmalıdır", "öğeden az içermelidir", " değerinden küçük olmalıdır")
	case "eq":
		return p(0) + " olmalıdır"
	case "ne":
		return p(0) + " olmamalıdır"
	case "oneof":
		return "şunlardan biri olmalıdır: " + strings.Join(e.Params, ", ")
	case "email":
		return "geçerli bir e-posta adresi olmalıdır"
	case "url":
		return "geçerli bir URL olmalıdır"
	case "uuid":
		return "geçerli bir UUID olmalıdır"
	case "ip":
		return "bir IP adresi olmalıdır"
	case "ipv4":
		return "bir IPv4 adresi olmalıdır"
	case "ipv6":
		return "bir IPv6 adresi olmalıdır"
	case "hostname":
		return "geçerli bir alan adı olmalıdır"
	case "alpha":
		return "yalnızca harf içermelidir"
	case "alphanum":
		return "yalnızca harf ve rakam içermelidir"
	case "numeric":
		return "bir sayı olmalıdır"
	case "lowercase":
		return "küçük harfle yazılmalıdır"
	case "uppercase":
		return "büyük harfle yazılmalıdır"
	case "contains":
		return `"` + p(0) + `" içermelidir`
	case "excludes":
		return `"` + p(0) + `" içermemelidir`
	case "startswith":
		return `"` + p(0) + `" ile başlamalıdır`
	case "endswith":
		return `"` + p(0) + `" ile bitmelidir`
	case "slug":
		return "kısa çizgiyle ayrılmış küçük harfli kelimelerden oluşmalıdır"
	case "unique":
		return "aynı değeri iki kez içermemelidir"
	case "type":
		return "türü yanlış"
	case "unknown":
		return "bu isteğin bir alanı değildir"
	case "max_size":
		return "en fazla " + p(0) + " bayt olmalıdır"
	case "file_type":
		return "şu türlerden biri olmalıdır: " + strings.Join(e.Params, ", ")
	case "unreadable":
		return "okunamadı"
	}
	return ""
}
