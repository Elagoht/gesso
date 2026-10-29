package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const buildHelp = `Runs the go build somebody would otherwise have to remember:

  CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"

CGO off, so the binary is static and goes into an image with nothing else;
-trimpath, so it carries no paths of the machine that built it; -s -w, to drop
the debug tables. The target is this machine unless -os and -arch name another:
a binary built on a Mac does not run in a Linux container ("-os linux -arch
amd64"). Beside it the binary needs only its .env.`

func buildCommand(args []string, dir string, stdout io.Writer, stdin io.Reader) error {
	fs := newFlags("build")
	out := fs.String("o", "", `where to write the binary (default "bin/<module name>")`)
	goos := fs.String("os", runtime.GOOS, "target operating system")
	goarch := fs.String("arch", runtime.GOARCH, "target architecture")
	pkg := fs.String("pkg", ".", "the main package")
	ask := fs.Bool("i", false, "ask whether to write a Dockerfile and a systemd unit beside the binary")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usage("build takes no arguments")
	}
	mod, err := readModule(dir)
	if err != nil {
		return err
	}
	name := path.Base(mod)
	target := *out
	if target == "" {
		target = filepath.Join("bin", name)
	}
	var buildOut bytes.Buffer
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", target, *pkg)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &buildOut, &buildOut
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+*goos, "GOARCH="+*goarch)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the build failed:\n%s", buildOut.String())
	}
	fmt.Fprintf(stdout, "built %s (%s/%s)\n", target, *goos, *goarch)
	if !*ask {
		return nil
	}
	in := bufio.NewReader(stdin)
	beside := filepath.Dir(filepath.Join(dir, target))
	for _, f := range []struct{ question, file, body string }{
		{"Write a Dockerfile?", "Dockerfile", dockerfileFor(name, *pkg)},
		{"Write a systemd unit?", name + ".service", systemdUnitFor(name)},
	} {
		fmt.Fprintf(stdout, "%s [y/N] ", f.question)
		line, _ := in.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			continue
		}
		p := filepath.Join(beside, f.file)
		if _, err := os.Stat(p); err == nil {
			fmt.Fprintf(stdout, "kept %s: it exists\n", p)
			continue
		}
		if err := os.WriteFile(p, []byte(f.body), 0o644); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "wrote", p)
		if f.file == "Dockerfile" {
			rel, _ := filepath.Rel(dir, p)
			fmt.Fprintf(stdout, "  docker build -f %s .\n", rel)
		}
	}
	return nil
}

func dockerfileFor(name, pkg string) string {
	return `# Written by "gesso build -i". Build it from the project's root:
#   docker build -f <this file> .
FROM golang:` + goMinor() + ` AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /` + name + ` ` + pkg + `

FROM gcr.io/distroless/static-debian12
# The program reads .env from here: mount it, or set the variables instead.
WORKDIR /srv
COPY --from=build /` + name + ` /usr/local/bin/` + name + `
ENV ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/` + name + `"]
`
}

func systemdUnitFor(name string) string {
	return `# Written by "gesso build -i". Install it as /etc/systemd/system/` + name + `.service,
# adjust User and the paths, then: systemctl daemon-reload && systemctl enable --now ` + name + `
[Unit]
Description=` + name + `
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=` + name + `
# The program reads .env from here.
WorkingDirectory=/srv/` + name + `
ExecStart=/usr/local/bin/` + name + `
Restart=on-failure
RestartSec=2
# gesso drains in-flight requests on SIGTERM; give it the time.
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
`
}

// goMinor is the Go a generated Dockerfile builds with: this CLI's, major and
// minor, so the image takes patch releases.
func goMinor() string {
	parts := strings.SplitN(strings.TrimPrefix(runtime.Version(), "go"), ".", 3)
	if len(parts) < 2 {
		return parts[0]
	}
	return parts[0] + "." + parts[1]
}
