package instance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestMetricsCountsRejectedAsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("process_resident_memory_bytes 1024\n" +
			`comfyui_prompts_total{status="success"} 12` + "\n" +
			`comfyui_prompts_total{status="error"} 2` + "\n" +
			`comfyui_prompts_total{status="rejected"} 44` + "\n" +
			`comfyui_prompts_total{status="interrupted"} 3` + "\n" +
			"# TYPE comfyui_custom_node_import_failed gauge\n" +
			`comfyui_custom_node_import_failed{node="comfyui-impact-subpack"} 1` + "\n" +
			`comfyui_custom_node_import_failed{node="nkg_broken"} 1` + "\n"))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	m, ok, err := Client{Port: port}.Metrics(context.Background())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if m.OK != 12 || m.Err != 46 || m.RSS != 1024 {
		t.Errorf("metrics = %+v, want OK 12, Err 46 (error + rejected, not interrupted)", m)
	}
	if strings.Join(m.Failed, ",") != "comfyui-impact-subpack,nkg_broken" {
		t.Errorf("failed nodes = %q", m.Failed)
	}
}
