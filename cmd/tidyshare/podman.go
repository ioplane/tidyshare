package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.podman.io/podman/v6/pkg/bindings"
	"go.podman.io/podman/v6/pkg/bindings/images"
	"go.podman.io/podman/v6/pkg/bindings/system"
)

func socketPath(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid Podman endpoint: %w", err)
	}
	if u.Scheme != "unix" || u.Host != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !filepath.IsAbs(u.Path) || filepath.Clean(u.Path) != u.Path || strings.ContainsAny(u.Path, "\x00\r\n") {
		return "", errors.New("podman endpoint must be a local Unix socket URI without credentials, query or fragment")
	}
	return u.Path, nil
}

func parseImageSockets(value, owners string) (map[string]string, error) {
	allowed := map[string]bool{"root": true}
	for _, owner := range splitList(owners) {
		allowed[owner] = true
	}
	out := map[string]string{}
	for _, item := range splitList(value) {
		owner, endpoint, ok := strings.Cut(item, "=")
		if !ok || !allowed[owner] || out[owner] != "" {
			return nil, errors.New("image-sockets requires unique requested owner=unix:///path entries")
		}
		if _, err := socketPath(endpoint); err != nil {
			return nil, err
		}
		out[owner] = endpoint
	}
	return out, nil
}

type podmanAPIError struct {
	operation string
	cause     error
}

func (e *podmanAPIError) Error() string { return "podman " + e.operation + " request failed" }
func (e *podmanAPIError) Unwrap() error { return e.cause }

type podmanReadTransport struct {
	*http.Transport
	counts map[string]int
}

func (t podmanReadTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		return nil, errors.New("podman inventory permits GET requests only")
	}
	response, err := t.Transport.RoundTrip(r)
	if err == nil {
		response.Body = http.MaxBytesReader(nil, response.Body, 32<<20)
		if response.StatusCode == http.StatusOK && strings.HasSuffix(r.URL.Path, "/libpod/images/json") {
			body, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			// SDK scalar fields lose presence; an omitted count never proves zero.
			var presence []struct {
				ID         string `json:"Id"`
				Containers *int
			}
			if err := json.Unmarshal(body, &presence); err != nil {
				return nil, err
			}
			for _, p := range presence {
				t.counts[p.ID] = -1
				if p.Containers != nil {
					t.counts[p.ID] = *p.Containers
				}
			}
			response.Body = io.NopCloser(bytes.NewReader(body))
		}
	}
	return response, err
}

func podmanOwnerSocket(owner, endpoint string) (string, uint64, error) {
	if owner == "root" && os.Geteuid() != 0 {
		return "", 0, errors.New("root store requires root privileges")
	}
	u, err := user.Lookup(owner)
	if err != nil {
		return "", 0, err
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return "", 0, fmt.Errorf("invalid Podman owner: %w", err)
	}
	if os.Geteuid() != 0 && uint64(os.Geteuid()) != uid {
		return "", 0, errors.New("podman store requires its owner or root privileges")
	}
	if endpoint == "" {
		path := "/run/podman/podman.sock"
		if uid != 0 {
			path = filepath.Join("/run/user", u.Uid, "podman/podman.sock")
		}
		endpoint = "unix://" + path
	}
	path, err := socketPath(endpoint)
	if err != nil {
		return "", 0, err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return "", 0, fmt.Errorf("podman API unavailable; start the owner's service separately or set -image-sockets %s=unix:///path: %w", owner, err)
	}
	if st.Mode()&os.ModeSocket == 0 || uint64(st.Sys().(*syscall.Stat_t).Uid) != uid || st.Mode().Perm()&0o002 != 0 {
		return "", 0, errors.New("podman socket must be a socket owned by the requested owner and not writable by others")
	}
	return endpoint, uid, nil
}

func listLocal(endpoint, owner string) ([]localImage, string, error) {
	endpoint, uid, err := podmanOwnerSocket(owner, endpoint)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := bindings.NewConnection(ctx, endpoint)
	if err != nil {
		return nil, "", &podmanAPIError{"connect", err}
	}
	client, err := bindings.GetClient(conn)
	if err != nil {
		return nil, "", err
	}
	defer client.Client.CloseIdleConnections()
	transport, ok := client.Client.Transport.(*http.Transport)
	if !ok {
		return nil, "", errors.New("unexpected Podman SDK transport")
	}
	counts := map[string]int{}
	client.Client.Transport = podmanReadTransport{transport, counts}
	client.Client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("podman inventory rejects redirects") }
	info, err := system.Info(conn, nil)
	if err != nil {
		return nil, "", &podmanAPIError{"store identity", err}
	}
	if info == nil || info.Host == nil || info.Store == nil || info.Store.GraphRoot == "" || info.Store.RunRoot == "" {
		return nil, "", errors.New("podman API returned incomplete store identity")
	}
	if info.Host.Security.Rootless != (uid != 0) {
		return nil, "", errors.New("podman rootless identity does not match the requested owner")
	}
	raw, err := images.List(conn, new(images.ListOptions).WithAll(true))
	if err != nil {
		return nil, "", &podmanAPIError{"images", err}
	}
	var imgs []localImage
	for _, x := range raw {
		if x == nil || x.ID == "" {
			return nil, "", errors.New("podman API returned an image without identity")
		}
		var tags []string
		for _, names := range [][]string{x.Names, x.RepoTags} {
			tags = slices.DeleteFunc(slices.Clone(names), func(s string) bool { return s == "" || s == "<none>" || s == "<none>:<none>" })
			if len(tags) > 0 {
				break
			}
		}
		imgs = append(imgs, localImage{ID: strings.TrimPrefix(x.ID, "sha256:"), Owner: owner, Tags: tags, Size: x.Size, Created: x.Created, Dangling: x.Dangling, Containers: counts[x.ID]})
	}
	summary := fmt.Sprintf("endpoint: %s\ngraphroot: %s\nrunroot: %s\n", endpoint, info.Store.GraphRoot, info.Store.RunRoot)
	df, err := system.DiskUsage(conn, nil)
	if err != nil || df == nil {
		return imgs, summary + fmt.Sprintf("images: %d\ndisk usage: unavailable\n", len(raw)), nil
	}
	// Summarise counts only; container commands can contain credentials.
	summary += fmt.Sprintf("images: %d; layer bytes: %d\ncontainers: %d\nvolumes: %d\n", len(raw), df.ImagesSize, len(df.Containers), len(df.Volumes))
	return imgs, summary, nil
}
