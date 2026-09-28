package category

import (
	"testing"
	"testing/fstest"
)

func TestDefaultRules(t *testing.T) {
	m, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"ds-prod-gz-20.df.qq.com":                      "三角洲行动",
		"lobby-prod.df.qq.com":                         "三角洲行动",
		"g79-102.gph.netease.com":                      "我的世界（网易）",
		"g79.update.netease.com":                       "我的世界（网易）",
		"mc-launcher.webapp.163.com":                   "我的世界（网易）",
		"g0.gsf.netease.com":                           "网易游戏（通用）",
		"grpc.snm0516.aisee.tv":                        "云视听极光",
		"hik-alicloud-hn.oss-cn-shenzhen.aliyuncs.com": "萤石云",
		"static.gc.apple.com":                          "Game Center",
		"amp-api-edge.apps.apple.com":                  "App Store",
		"xp.apple.com":                                 "苹果服务",
		"aweme.snssdk.com":                             "抖音",
		"mon.snssdk.com":                               "字节统计",
		"finder.video.qq.com":                          "微信视频号",
		"vv.video.qq.com":                              "腾讯视频",
		"hunantv.m.cn.miaozhen.com":                    "秒针监测",
		"WWW.BILIBILI.COM.":                            "哔哩哔哩",
	}
	for domain, want := range cases {
		i, ok := m.Match(domain)
		if !ok || m.App(i).Name != want {
			t.Errorf("%s: got %v %q, want %q", domain, ok, name(m, i, ok), want)
		}
	}
	for _, domain := range []string{"qq.com", "restlbs.qq.com.example", "", "sub.unknown-site.org"} {
		if i, ok := m.Match(domain); ok {
			t.Errorf("%s unexpectedly matched %q", domain, m.App(i).Name)
		}
	}
	if i, _ := m.Match("ds-prod-gz-20.df.qq.com"); !m.Categories()[m.App(i).Category].Entertainment {
		t.Error("game must count as entertainment")
	}
	if h, ok := m.Hint("osfsr.lenovomm.com"); !ok || h != "联想设备" {
		t.Errorf("hint = %q %v", h, ok)
	}
	if m.CategoryIndex(Other) < 0 || m.CategoryIndex(Unknown) < 0 || m.CategoryIndex("nope") != -1 {
		t.Error("category index")
	}
}

func name(m *Matcher, i int, ok bool) string {
	if !ok {
		return ""
	}
	return m.App(i).Name
}

func TestLoadRejectsBadRules(t *testing.T) {
	base := `{"categories":[{"id":"other","name":"其他"},{"id":"unknown","name":"未识别"},{"id":"game","name":"游戏"}]}`
	cases := map[string]string{
		"duplicate domain": `{"apps":[{"name":"a","category":"game","domains":["x.com"]},{"name":"b","category":"game","domains":["X.com"]}]}`,
		"unknown category": `{"apps":[{"name":"a","category":"nope","domains":["x.com"]}]}`,
	}
	for name, apps := range cases {
		fsys := fstest.MapFS{
			"a.json": {Data: []byte(base)},
			"b.json": {Data: []byte(apps)},
		}
		if _, err := Load(fsys); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := Load(fstest.MapFS{"a.json": {Data: []byte(`{"categories":[{"id":"game","name":"游戏"}]}`)}}); err == nil {
		t.Error("missing required categories must fail")
	}
}

func TestRegistrableDomain(t *testing.T) {
	cases := map[string]string{
		"a.b.example.com":                    "example.com",
		"example.com":                        "example.com",
		"x.lenovo.com.cn":                    "lenovo.com.cn",
		"only-557918.nstool.laiqukankan.com": "laiqukankan.com",
		"localhost":                          "localhost",
		"cdn.example.io":                     "example.io",
	}
	for in, want := range cases {
		if got := RegistrableDomain(in); got != want {
			t.Errorf("%s = %s, want %s", in, got, want)
		}
	}
}
