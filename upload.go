package gesso

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/Elagoht/gesso/validate"
)

// A handler takes a form — files and fields from a multipart/form-data body, or,
// for an input with no file, fields from an application/x-www-form-urlencoded
// one (at most 1 MiB) — through form tags:
//
//	type UploadIn struct {
//		Title  string        `form:"title" validate:"required,max=100"`
//		Avatar *gesso.File   `form:"avatar" accept:"image/png,image/jpeg" maxsize:"2MiB" validate:"required"`
//		Extra  []*gesso.File `form:"extra" maxsize:"10MiB"`
//	}
//
// An input reads a form or a JSON body, not both. A form's fields take the types
// a query parameter does. A file's type is read from its first bytes, not taken
// from what the client says; accept lists the types allowed ("image/*" for any
// image), maxsize bounds each file (B, KB, KiB, MB, MiB, GB, GiB). The whole
// body is bounded by MaxBodyBytes, or the route's MaxBody. A key the input does
// not have is refused, as in a JSON body. The files are kept in memory or, past
// 32 MiB, in temporary files, removed when the handler returns.

// File is an uploaded file.
type File struct {
	// Filename is what the client named it, without any folders. It is the
	// client's word: never use it as a path.
	Filename string
	// ContentType is what the first bytes say the file is: "image/png",
	// "application/pdf", "text/plain; charset=utf-8".
	ContentType string
	Size        int64
	open        func() (io.ReadCloser, error)
}

// Open reads the file.
func (f *File) Open() (io.ReadCloser, error) { return f.open() }

// NewFile is a file made from data, for a caller's Endpoint.Call and for tests.
func NewFile(filename string, data []byte) *File {
	return &File{
		Filename: filename, ContentType: http.DetectContentType(data), Size: int64(len(data)),
		open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
	}
}

// ErrInvalidForm is a body that does not parse as the form it says it is.
var ErrInvalidForm = BadRequest("invalid_form", "the body is not a valid form")

// MaxBody bounds this route's request body to n bytes instead of the app's
// MaxBodyBytes: an upload route needs more.
func MaxBodyBytes(n int64) HandleOption {
	return func(c *handleConfig) { c.maxBody = n }
}

var (
	fileType  = reflect.TypeFor[*File]()
	filesType = reflect.TypeFor[[]*File]()
)

type formPlan struct {
	index   []int
	name    string
	file    bool
	multi   bool
	ptr     bool
	parse   func(string) (reflect.Value, error)
	accept  []string
	maxSize int64
}

func planForm(f reflect.StructField, index []int) (formPlan, error) {
	fp := formPlan{index: index, name: f.Tag.Get("form")}
	if fp.name == "" {
		return fp, errors.New("the form tag names no field")
	}
	if hasTag(f, "path") || hasTag(f, "query") || hasTag(f, "header") || hasTag(f, "json") {
		return fp, errors.New("a field takes one of form, path, query, header or json")
	}
	switch f.Type {
	case fileType:
		fp.file = true
	case filesType:
		fp.file, fp.multi = true, true
	}
	if fp.file {
		if a := f.Tag.Get("accept"); a != "" {
			for t := range strings.SplitSeq(a, ",") {
				fp.accept = append(fp.accept, strings.ToLower(strings.TrimSpace(t)))
			}
		}
		if m := f.Tag.Get("maxsize"); m != "" {
			n, err := parseSize(m)
			if err != nil {
				return fp, fmt.Errorf("maxsize %q: %w", m, err)
			}
			fp.maxSize = n
		}
		return fp, nil
	}
	if hasTag(f, "accept") || hasTag(f, "maxsize") {
		return fp, errors.New("accept and maxsize are for files")
	}
	t := f.Type
	if t.Kind() == reflect.Slice && t.Elem().Kind() != reflect.Uint8 {
		fp.multi, t = true, t.Elem()
	} else if t.Kind() == reflect.Pointer {
		fp.ptr, t = true, t.Elem()
	}
	parse, err := parserFor(t)
	if err != nil {
		return fp, err
	}
	fp.parse = parse
	return fp, nil
}

// parseSize reads "2MiB", "500KB", "1024".
func parseSize(s string) (int64, error) {
	units := []struct {
		suffix string
		n      int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1}}
	mult := int64(1)
	for _, u := range units {
		if rest, ok := strings.CutSuffix(s, u.suffix); ok {
			s, mult = strings.TrimSpace(rest), u.n
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("want a size: 2MiB, 500KB, 1024")
	}
	return n * mult, nil
}

const (
	// formMemory is how much of a multipart form is kept in memory; the rest
	// goes to temporary files.
	formMemory = 32 << 20
	// urlencodedMax bounds a urlencoded form, which is read whole into memory.
	urlencodedMax = 1 << 20
)

func (p *inputPlan) hasFiles() bool {
	return slices.ContainsFunc(p.form, func(fp formPlan) bool { return fp.file })
}

// decodeForm fills the form fields of dst from r's body.
func (p *inputPlan) decodeForm(r *http.Request, dst reflect.Value, maxBody int64) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return ErrUnsupportedMedia.WithDetail("the body must be multipart/form-data or application/x-www-form-urlencoded")
	}
	var values map[string][]string
	var files map[string][]*multipart.FileHeader
	switch {
	case mt == "multipart/form-data":
		if !p.hasFiles() {
			// Fields only: they are kept in memory, so the route's larger
			// limit, which is for files, does not apply.
			maxBody = min(maxBody, urlencodedMax)
		}
		r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
		err = r.ParseMultipartForm(min(formMemory, maxBody))
		if err == nil {
			values, files = r.MultipartForm.Value, r.MultipartForm.File
		}
	case mt == "application/x-www-form-urlencoded" && !p.hasFiles():
		// Read whole into memory: bounded by the app's own limit, never by an
		// upload route's larger one, which is for files on disk.
		r.Body = http.MaxBytesReader(nil, r.Body, min(maxBody, urlencodedMax))
		err = r.ParseForm()
		values = r.PostForm
	case p.hasFiles():
		return ErrUnsupportedMedia.WithDetail("the body must be multipart/form-data")
	default:
		return ErrUnsupportedMedia.WithDetail("the body must be multipart/form-data or application/x-www-form-urlencoded")
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ErrBodyTooLarge
		}
		return ErrInvalidForm.Wrap(err)
	}
	var fields []validate.FieldError
	known := map[string]bool{}
	for _, fp := range p.form {
		known[fp.name] = true
		field := dst.FieldByIndex(fp.index)
		if fp.file {
			got, errs := openFiles(fp, files[fp.name])
			fields = append(fields, errs...)
			if len(got) == 0 {
				continue
			}
			if fp.multi {
				field.Set(reflect.ValueOf(got))
			} else {
				field.Set(reflect.ValueOf(got[0]))
			}
			continue
		}
		vs := values[fp.name]
		if len(vs) == 0 {
			continue
		}
		if !fp.multi {
			vs = vs[:1]
		}
		parsed := make([]reflect.Value, 0, len(vs))
		for _, s := range vs {
			v, err := fp.parse(s)
			if err != nil {
				fields = append(fields, validate.FieldError{Field: fp.name, Code: "type", Message: err.Error()})
				break
			}
			parsed = append(parsed, v)
		}
		if len(parsed) == len(vs) {
			setParsed(field, paramPlan{multi: fp.multi, ptr: fp.ptr}, parsed)
		}
	}
	for k := range values {
		if !known[k] {
			fields = append(fields, validate.FieldError{Field: k, Code: "unknown", Message: "is not a field of this request"})
		}
	}
	for k := range files {
		if !known[k] {
			fields = append(fields, validate.FieldError{Field: k, Code: "unknown", Message: "is not a field of this request"})
		}
	}
	if fields != nil {
		slices.SortFunc(fields, func(a, b validate.FieldError) int { return strings.Compare(a.Field, b.Field) })
		return ErrInvalidInput.WithFields(fields...)
	}
	return nil
}

// openFiles makes Files of a field's parts, sniffing each one's type and
// holding it to the field's accept and maxsize.
func openFiles(fp formPlan, parts []*multipart.FileHeader) ([]*File, []validate.FieldError) {
	if len(parts) > 1 && !fp.multi {
		return nil, []validate.FieldError{{Field: fp.name, Code: "max", Params: []string{"1"}, Message: "must be one file"}}
	}
	var out []*File
	var errs []validate.FieldError
	for i, fh := range parts {
		name := fp.name
		if fp.multi {
			name += "[" + strconv.Itoa(i) + "]"
		}
		if fp.maxSize > 0 && fh.Size > fp.maxSize {
			errs = append(errs, validate.FieldError{Field: name, Code: "max_size", Params: []string{strconv.FormatInt(fp.maxSize, 10)},
				Message: "must be at most " + strconv.FormatInt(fp.maxSize, 10) + " bytes"})
			continue
		}
		f, err := fh.Open()
		if err != nil {
			errs = append(errs, validate.FieldError{Field: name, Code: "unreadable", Message: "could not be read"})
			continue
		}
		head := make([]byte, 512)
		n, _ := io.ReadFull(f, head)
		_ = f.Close()
		ctype := http.DetectContentType(head[:n])
		if len(fp.accept) > 0 && !accepted(ctype, fp.accept) {
			errs = append(errs, validate.FieldError{Field: name, Code: "file_type", Params: fp.accept,
				Message: "must be one of: " + strings.Join(fp.accept, ", ")})
			continue
		}
		out = append(out, &File{
			Filename: cleanFilename(fh.Filename), ContentType: ctype, Size: fh.Size,
			open: func() (io.ReadCloser, error) { return fh.Open() },
		})
	}
	return out, errs
}

func accepted(ctype string, accept []string) bool {
	mt, _, _ := mime.ParseMediaType(ctype)
	for _, a := range accept {
		if a == mt || (strings.HasSuffix(a, "/*") && strings.HasPrefix(mt, strings.TrimSuffix(a, "*"))) {
			return true
		}
	}
	return false
}

// cleanFilename is a client's file name without the folders some browsers and
// many attackers put before it.
func cleanFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == "/" || name == ".." {
		return ""
	}
	return name
}

// encodeForm writes the form fields of v as a multipart body, for a Remote.
func encodeForm(p *inputPlan, v reflect.Value) (io.Reader, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, fp := range p.form {
		f := v.FieldByIndex(fp.index)
		if fp.file {
			var files []*File
			if fp.multi {
				files, _ = f.Interface().([]*File)
			} else if file, _ := f.Interface().(*File); file != nil {
				files = []*File{file}
			}
			for _, file := range files {
				if err := writeFilePart(mw, fp.name, file); err != nil {
					return nil, "", err
				}
			}
			continue
		}
		values, err := formatParam(f, paramPlan{multi: fp.multi, ptr: fp.ptr})
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", fp.name, err)
		}
		for _, s := range values {
			if err := mw.WriteField(fp.name, s); err != nil {
				return nil, "", err
			}
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return &buf, mw.FormDataContentType(), nil
}

func writeFilePart(mw *multipart.Writer, field string, file *File) error {
	part, err := mw.CreateFormFile(field, file.Filename)
	if err != nil {
		return err
	}
	rc, err := file.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(part, rc)
	return err
}
