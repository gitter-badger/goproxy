package autorange

import (
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/golang/glog"

	"../../../httpproxy"
	"../../../storage"
	"../../filters"
	"../../transport"
)

const (
	filterName string = "autorange"
)

type Config struct {
	Sites   []string
	Paths   []string
	MaxSize int
	BufSize int
	Threads int
}

type Filter struct {
	SiteMatcher *httpproxy.HostMatcher
	PathMatcher *httpproxy.HostMatcher
	MaxSize     int
	BufSize     int
	Threads     int
}

func init() {
	filename := filterName + ".json"
	config := new(Config)
	err := storage.ReadJsonConfig(filters.LookupConfigStoreURI(filterName), filename, config)
	if err != nil {
		glog.Fatalf("storage.ReadJsonConfig(%#v) failed: %s", filename, err)
	}

	err = filters.Register(filterName, &filters.RegisteredFilter{
		New: func() (filters.Filter, error) {
			return NewFilter(config)
		},
	})

	if err != nil {
		glog.Fatalf("Register(%#v) error: %s", filterName, err)
	}
}

func NewFilter(config *Config) (filters.Filter, error) {
	f := &Filter{
		SiteMatcher: httpproxy.NewHostMatcher(config.Sites),
		PathMatcher: httpproxy.NewHostMatcher(config.Paths),
		MaxSize:     config.MaxSize,
		BufSize:     config.BufSize,
		Threads:     config.Threads,
	}

	return f, nil
}

func (f *Filter) FilterName() string {
	return filterName
}

func (f *Filter) Request(ctx *filters.Context, req *http.Request) (*filters.Context, *http.Request, error) {
	if req.Method != http.MethodGet || strings.Contains(req.URL.RawQuery, "range=") {
		return ctx, req, nil
	}

	if r := req.Header.Get("Range"); r == "" {
		switch {
		case f.SiteMatcher.Match(req.Host):
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", 0, f.MaxSize))
			glog.V(2).Infof("AUTORANGE Sites rule matched, add %s for\"%s\"", req.Header.Get("Range"), req.URL.String())
		case f.PathMatcher.Match(path.Base(req.URL.Path)):
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", 0, f.MaxSize))
			glog.V(2).Infof("AUTORANGE Paths rule matched, add %s for\"%s\"", req.Header.Get("Range"), req.URL.String())
		}
	} else {
		parts := strings.Split(r, " ")
		switch parts[0] {
		case "bytes":
			parts1 := strings.Split(parts[1], "-")
			if start, err := strconv.Atoi(parts1[0]); err == nil {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+f.MaxSize))
				glog.V(2).Infof("AUTORANGE Default rule matched, change %s to %s for\"%s\"", r, req.Header.Get("Range"), req.URL.String())
			}
		default:
			glog.Warningf("AUTORANGE Default rule matched, but cannot support %#v range for \"%s\"", r, req.URL.String())
		}
	}

	return ctx, req, nil
}

func (f *Filter) Response(ctx *filters.Context, resp *http.Response) (*filters.Context, *http.Response, error) {
	if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Length") == "" {
		return ctx, resp, nil
	}

	f1 := ctx.GetRoundTripFilter()
	if f1 == nil {
		return ctx, resp, nil
	}

	parts := strings.Split(resp.Header.Get("Content-Range"), " ")
	if len(parts) != 2 || parts[0] != "bytes" {
		return ctx, resp, nil
	}

	parts1 := strings.Split(parts[1], "/")
	parts2 := strings.Split(parts1[0], "-")
	if len(parts1) != 2 || len(parts2) != 2 {
		return ctx, resp, nil
	}

	var end, length int64
	var err error

	end, err = strconv.ParseInt(parts2[1], 10, 64)
	if err != nil {
		return ctx, resp, nil
	}

	length, err = strconv.ParseInt(parts1[1], 10, 64)
	if err != nil {
		return ctx, resp, nil
	}

	glog.V(2).Infof("AUTORANGE respone matched, start rangefetch for %#v", resp.Header.Get("Content-Range"))

	resp.ContentLength = length
	resp.Header.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	resp.Header.Del("Content-Range")

	r, w := httpproxy.IoPipe()

	go func(w *httpproxy.PipeWriter, filter filters.RoundTripFilter, req *http.Request, start, length int64) {
		glog.V(2).Infof("AUTORANGE begin rangefetch for %#v by using %#v", req.URL.String(), filter.FilterName())
		w.Close()
	}(w, f1, resp.Request, end+1, length)

	resp.Body = transport.NewMultiReadCloser(resp.Body, r)

	return ctx, resp, nil
}
