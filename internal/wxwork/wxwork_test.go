package wxwork

import (
	"net/http"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/silenceper/wechat/v2/util"
)

func TestInitConfiguresBoundedSDKHTTPClient(t *testing.T) {
	originalClient := util.DefaultHTTPClient
	originalConfig := config.GetCurrent()
	originalWork := w
	originalWxConfig := wxCfg
	t.Cleanup(func() {
		util.DefaultHTTPClient = originalClient
		config.SetCurrent(originalConfig)
		w = originalWork
		wxCfg = originalWxConfig
	})

	util.DefaultHTTPClient = http.DefaultClient
	config.SetCurrent(&config.Config{WxWork: config.WxWorkConfig{
		Enabled:    true,
		CorpID:     "synthetic-corp",
		CorpSecret: "synthetic-secret",
	}})
	Init()

	if util.DefaultHTTPClient == nil || util.DefaultHTTPClient.Timeout != 30*time.Second {
		got := time.Duration(0)
		if util.DefaultHTTPClient != nil {
			got = util.DefaultHTTPClient.Timeout
		}
		t.Fatalf("WxWork SDK HTTP timeout = %s, want 30s", got)
	}
}
