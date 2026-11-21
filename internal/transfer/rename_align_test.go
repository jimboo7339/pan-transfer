package transfer

import (
	"context"
	"testing"

	"pan-transfer/internal/domain"
	"pan-transfer/internal/driver"
)

// renameAlignDriver 是一个只实现 Lister + Renamer 的最小驱动，
// 用于验证 renameFiles 的「按名字对齐」行为。
//
// 关键点：它故意把 ListFiles 返回的顺序设计成与 plan 顺序**不一致**，
// 模拟真实驱动的返回顺序（光鸭靠 diff 目录、夸克靠异步 task，
// 都不保证与请求顺序一致）。
type renameAlignDriver struct {
	// files 是目标目录当前内容（fid -> name）
	files map[string]string
	// renamed 记录实际发生的改名：fid -> 新名字
	renamed map[string]string
}

func (d *renameAlignDriver) Config() driver.Config {
	return driver.Config{Name: "fake", DisplayName: "假驱动"}
}

func (d *renameAlignDriver) GetAddition() any { return nil }

func (d *renameAlignDriver) Init(ctx context.Context) error { return nil }

func (d *renameAlignDriver) Drop(ctx context.Context) error { return nil }

func (d *renameAlignDriver) Ping(ctx context.Context) error { return nil }

func (d *renameAlignDriver) ListFiles(ctx context.Context, parentID string) ([]domain.FileItem, error) {
	items := make([]domain.FileItem, 0, len(d.files))
	for fid, name := range d.files {
		items = append(items, domain.FileItem{ID: fid, Name: name, IsDir: false})
	}
	// 故意反转顺序：真实驱动不保证与 plan 顺序一致
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items, nil
}

func (d *renameAlignDriver) RenameFile(ctx context.Context, fileID, newName string) error {
	d.renamed[fileID] = newName
	d.files[fileID] = newName
	return nil
}

// 以下方法只为满足 driver.ShareTransferer 接口（NewDramaExecutor 要求），
// 本测试只调用 renameFiles，不会走到这些路径。

func (d *renameAlignDriver) ExtractShareURL(shareURL string) (string, string, string, error) {
	return "", "", "", nil
}

func (d *renameAlignDriver) GetShareToken(ctx context.Context, pwdID, passcode string) (string, error) {
	return "", nil
}

func (d *renameAlignDriver) ListShareItems(ctx context.Context, pwdID, stoken, pdirFID string) ([]driver.ShareEntry, error) {
	return nil, nil
}

func (d *renameAlignDriver) SaveShareFiles(ctx context.Context, req driver.SaveShareReq) (string, []string, error) {
	return "", nil, nil
}

func (d *renameAlignDriver) QueryTransferTask(ctx context.Context, taskID string) (driver.TransferStatus, error) {
	return driver.TransferStatus{}, nil
}

func (d *renameAlignDriver) ResolvePathToFID(ctx context.Context, paths []string) ([]driver.PathFID, error) {
	return nil, nil
}

// TestRenameFilesAlignsByNameNotIndex 验证重命名按**文件名**对齐，而不是按下标。
//
// 这是本次审查里最危险的一条：
// 早先 renameFiles 用 savedFIDs[i] 配 plan[i]，隐含假设驱动
// "严格按请求顺序一一返回 fid"。这不是契约保证——光鸭是同步 restore
// 靠 diff 目录对齐，夸克是异步 task，返回顺序都取决于服务端。
// 一旦顺序不一致，RenameFile 会把 A 文件改成 B 的名字，
// 静默的数据错乱，而日志里显示的还是正确的 origin->target。
func TestRenameFilesAlignsByNameNotIndex(t *testing.T) {
	// 目标目录：3 个文件，fid 与名字的对应故意与 plan 顺序错开
	drv := &renameAlignDriver{
		files: map[string]string{
			"fid-a": "ep01.mkv",
			"fid-b": "ep02.mkv",
			"fid-c": "ep03.mkv",
		},
		renamed: map[string]string{},
	}
	task := domainTask("")
	task.TaskName = "重命名对齐"
	e, err := NewDramaExecutor(drv, task, nil)
	if err != nil {
		t.Fatalf("构造执行器失败: %v", err)
	}

	// plan 按 ep03, ep01, ep02 的顺序（模拟上游乱序返回）
	plan := []dramaPlanItem{
		{fid: "whatever-3", originName: "ep03.mkv", targetName: "S01E03.mkv"},
		{fid: "whatever-1", originName: "ep01.mkv", targetName: "S01E01.mkv"},
		{fid: "whatever-2", originName: "ep02.mkv", targetName: "S01E02.mkv"},
	}
	// savedFIDs 顺序故意与 plan 不一致（这里干脆给错的名字对应的 fid）
	savedFIDs := []string{"fid-b", "fid-c", "fid-a"}

	renamed := e.renameFiles(context.Background(), savedFIDs, plan, "dest")

	if len(renamed) != 3 {
		t.Fatalf("应尝试重命名 3 个文件，得到 %d", len(renamed))
	}

	// 核心断言：fid 与名字的对应必须正确
	// ep01.mkv (fid-a) 应改成 S01E01.mkv，而不是 S01E03.mkv
	want := map[string]string{
		"fid-a": "S01E01.mkv",
		"fid-b": "S01E02.mkv",
		"fid-c": "S01E03.mkv",
	}
	for fid, wantName := range want {
		if got := drv.renamed[fid]; got != wantName {
			t.Errorf("%s（%s）应改名为 %q，得到 %q",
				fid, drv.files[fid], wantName, got)
		}
	}
}

// TestRenameFilesSkipsUnresolved 验证名字在目标目录找不到时跳过，
// 而不是拿 savedFIDs[i] 硬凑。
func TestRenameFilesSkipsUnresolved(t *testing.T) {
	drv := &renameAlignDriver{
		files: map[string]string{
			"fid-a": "ep01.mkv",
		},
		renamed: map[string]string{},
	}
	task := domainTask("")
	e, err := NewDramaExecutor(drv, task, nil)
	if err != nil {
		t.Fatalf("构造执行器失败: %v", err)
	}

	plan := []dramaPlanItem{
		{fid: "whatever", originName: "ep01.mkv", targetName: "S01E01.mkv"},
		// 这个文件在目标目录里不存在（转存失败或被上游改名）
		{fid: "whatever2", originName: "ep99.mkv", targetName: "S01E99.mkv"},
	}
	savedFIDs := []string{"fid-a", "fid-b"}

	renamed := e.renameFiles(context.Background(), savedFIDs, plan, "dest")

	if len(renamed) != 1 {
		t.Fatalf("应只重命名 1 个文件（另一个名字找不到），得到 %d", len(renamed))
	}
	if got := drv.renamed["fid-a"]; got != "S01E01.mkv" {
		t.Errorf("ep01.mkv 应改名为 S01E01.mkv，得到 %q", got)
	}
	// 关键：不能因为 savedFIDs[1] 存在就把 ep99 改名到 fid-b 上
	if _, wronglyRenamed := drv.renamed["fid-b"]; wronglyRenamed {
		t.Error("fid-b 不该被改名——ep99.mkv 在目标目录里不存在，应跳过")
	}
}