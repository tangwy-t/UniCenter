package service

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

// 本文件是「先删物理文件后删元数据 → 坏元数据残留」（评审报告 #16）
// 修复后的回归验证。
//
// 修复内容：DeleteMany 先删元数据、后删物理文件。DB 删除失败时物理文件
// 原样保留（不再产生"元数据指向已丢失文件"的坏记录）。

// pocFileRepo 记录 DeleteMany 的执行时序与 DeleteByIDs 是否可注入失败。
type pocFileRepo struct {
	findByIDsFiles []entity.SysFile
	deleteByIDsErr error
	deleteCalled   bool
}

func (r *pocFileRepo) FindPage(context.Context, *request.FileQuery) ([]entity.SysFile, int64, error) {
	return nil, 0, nil
}
func (r *pocFileRepo) FindByID(context.Context, uint64) (*entity.SysFile, error) { return nil, nil }
func (r *pocFileRepo) FindByIDs(context.Context, []uint64) ([]entity.SysFile, error) {
	return r.findByIDsFiles, nil
}
func (r *pocFileRepo) CreateBatch(context.Context, []*entity.SysFile) error { return nil }
func (r *pocFileRepo) Rename(context.Context, uint64, string) error         { return nil }
func (r *pocFileRepo) DeleteByIDs(context.Context, []uint64) error {
	r.deleteCalled = true
	return r.deleteByIDsErr
}
func (r *pocFileRepo) Stats(context.Context, time.Time) (*entity.FileStatsRow, error) {
	return nil, nil
}

// pocFileCfg 返回一个可写的临时上传目录。
type pocFileCfg struct{ dir string }

func (c *pocFileCfg) GetString(_ context.Context, key, _ string) string {
	if key == "sys.file.upload.path" {
		return c.dir
	}
	return ""
}
func (c *pocFileCfg) GetInt(context.Context, string, int) int { return 0 }

// 元数据删除失败时，物理文件必须保留（不再先删物理文件）。
func TestPoc_DeleteMany_MetadataDeleteFailure_KeepsPhysicalFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/deadbeefdeadbeef"
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}

	repo := &pocFileRepo{
		findByIDsFiles: []entity.SysFile{{StorageType: "local", Path: "deadbeefdeadbeef"}},
		deleteByIDsErr: errors.New("db delete failed"),
	}
	svc := NewFileService(repo, &pocFileCfg{dir: dir}, logger.NewNop())

	err := svc.DeleteMany(context.Background(), []uint64{1})
	if err == nil {
		t.Fatal("DeleteByIDs 失败时应向上返回错误")
	}

	// 物理文件必须仍在（元数据删除失败 → 不删物理文件）。
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		t.Fatal("元数据删除失败时物理文件不应被删（否则坏元数据残留）")
	}
}

// 元数据删除成功时，物理文件随后被清理。
func TestPoc_DeleteMany_MetadataDeleteSuccess_RemovesPhysicalFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/deadbeefdeadbeef"
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}

	repo := &pocFileRepo{
		findByIDsFiles: []entity.SysFile{{StorageType: "local", Path: "deadbeefdeadbeef"}},
	}
	svc := NewFileService(repo, &pocFileCfg{dir: dir}, logger.NewNop())

	if err := svc.DeleteMany(context.Background(), []uint64{1}); err != nil {
		t.Fatalf("DeleteMany 失败: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("元数据删除成功后物理文件应被清理")
	}
}
