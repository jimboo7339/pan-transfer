package transfer

import (
	"testing"
)

// TestResolutionTier 验证清晰度档位识别：忽略大小写、支持连写（2160p60fps）
// 和带分隔符的写法，HDR/60FPS 等干扰词不应改变档位。
func TestResolutionTier(t *testing.T) {
	cases := []struct {
		name string
		want int
	}{
		{"E01.1080p.WEB-DL.H265.AAC-XiaoXueshen.mp4", 2},
		{"E01.2160p.SDR.60fps.WEB-DL.H265.AAC-XiaoXueshen.mp4", 4},
		{"E01.2160P.SDR.60fps.mkv", 4},
		{"E01.4K.HDR.60FPS.高码率.mp4", 4},
		{"E01.UHD.BluRay.mkv", 4},
		{"E01.2K.WEB-DL.mkv", 3},
		{"E01.1440p.WEB-DL.mkv", 3},
		{"E01.720p.H264.mp4", 1},
		{"E01.1080I.ts", 2},
		{"E01.2160p60fps.mkv", 4},
		{"E01.WEB-DL.H265.AAC.mp4", 0}, // 无清晰度标记
		{"E01.480p.mp4", 0},            // 未列入档位的低清晰度
		{"E01.10800p.mp4", 0},          // 不能把 1080p 当成更长数字串的一部分
	}
	for _, c := range cases {
		if got := resolutionTier(c.name); got != c.want {
			t.Errorf("resolutionTier(%q) = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestBetterDedupCandidate 验证两种策略的胜者判定。
func TestBetterDedupCandidate(t *testing.T) {
	low := DedupCandidate{Name: "E01.1080p.mkv", Size: 200}
	tier4 := DedupCandidate{Name: "E01.2160p.mkv", Size: 861}
	tier3 := DedupCandidate{Name: "E01.2K.mkv", Size: 500}

	// 默认（含空值/未知值）：一律按大小，2160p 体积小也会输给 1080p 大文件。
	for _, strategy := range []string{"", "largest", "unknown"} {
		if betterDedupCandidate(low, tier4, strategy) {
			t.Errorf("strategy=%q 应按大小判定：200MB 不能胜过 861MB", strategy)
		}
		if !betterDedupCandidate(tier4, low, strategy) {
			t.Errorf("strategy=%q 应按大小判定：861MB 应胜过 200MB", strategy)
		}
	}

	// quality 策略：先按档位，高清晰度体积小也胜出；同档回退到大小。
	if !betterDedupCandidate(tier4, low, "quality") {
		t.Error("quality 策略下 2160p 应胜过 1080p（即使体积更大）")
	}
	if betterDedupCandidate(low, tier4, "quality") {
		t.Error("quality 策略下 1080p 不能胜过 2160p")
	}
	if betterDedupCandidate(tier3, tier4, "quality") {
		t.Error("quality 策略下 2K 不能胜过 4K")
	}
	if !betterDedupCandidate(tier4, tier3, "quality") {
		t.Error("quality 策略下 4K 应胜过 2K")
	}
	// 同档位：比大小，再比更新时间。
	if !betterDedupCandidate(DedupCandidate{Name: "E01.1080p.a.mkv", Size: 300}, low, "quality") {
		t.Error("同档位应按大小判定")
	}
	if !betterDedupCandidate(
		DedupCandidate{Name: "E01.1080p.a.mkv", Size: 200, UpdatedAt: 2},
		DedupCandidate{Name: "E01.1080p.b.mkv", Size: 200, UpdatedAt: 1},
		"quality",
	) {
		t.Error("同档同大小应按更新时间判定")
	}
}

// TestNormalizeDedupStrategyAndLabel 验证策略归一化与冲突文案。
func TestNormalizeDedupStrategyAndLabel(t *testing.T) {
	if got := normalizeDedupStrategy(" QUALITY "); got != DedupQuality {
		t.Errorf("大小写/空格归一化失败: %q", got)
	}
	if got := normalizeDedupStrategy(""); got != DedupLargest {
		t.Errorf("空值应回落到 largest: %q", got)
	}
	if got := conflictLabel("quality"); got != "重命名冲突（优先清晰度）" {
		t.Errorf("quality 冲突文案错误: %q", got)
	}
	if got := conflictLabel(""); got != "重命名冲突（保留最大）" {
		t.Errorf("默认冲突文案错误: %q", got)
	}
}
