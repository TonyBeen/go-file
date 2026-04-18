package model

import (
	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/sqlite"
	"go-file/common"
	"os"
	"path"
	"strings"
	"time"
)

type File struct {
	Id              int    `json:"id"`
	Filename        string `json:"filename"`
	Description     string `json:"description"`
	Uploader        string `json:"uploader"`
	Link            string `json:"link" gorm:"unique"`
	Time            string `json:"time"`
	DownloadCounter int    `json:"download_counter"`
	ExpireAt        string `json:"expire_at" gorm:"type:varchar(64);default:''"`
}

type LocalFile struct {
	Name         string
	Link         string
	Size         string
	IsFolder     bool
	ModifiedTime string
}

func AllFiles() ([]*File, error) {
	var files []*File
	var err error
	err = DB.Find(&files).Error
	return files, err
}

func QueryFiles(query string, startIdx int) ([]*File, error) {
	var files []*File
	var err error
	query = strings.ToLower(query)
	err = DB.Limit(common.ItemsPerPage).Offset(startIdx).Where("filename LIKE ? or description LIKE ? or uploader LIKE ? or time LIKE ?", "%"+query+"%", "%"+query+"%", "%"+query+"%", "%"+query+"%").Order("id desc").Find(&files).Error
	return files, err
}

func (file *File) Insert() error {
	var err error
	err = DB.Create(file).Error
	return err
}

// Delete Make sure link is valid! Because we will use os.Remove to delete it!
func (file *File) Delete() error {
	var err error
	err = DB.Delete(file).Error
	err = os.Remove(path.Join(common.UploadPath, file.Link))
	return err
}

func UpdateDownloadCounter(link string) {
	DB.Model(&File{}).Where("link = ?", link).UpdateColumn("download_counter", gorm.Expr("download_counter + 1"))
}

func DeleteExpiredFiles() {
	var files []*File
	now := time.Now().Format("2006-01-02 15:04:05")
	DB.Where("expire_at != '' AND expire_at < ?", now).Find(&files)
	for _, file := range files {
		file.Delete()
	}
}

func StartExpiredFileCleaner() {
	for {
		time.Sleep(15 * time.Second)
		DeleteExpiredFiles()
	}
}

func (file *File) DeleteIfExpired() bool {
	if file.ExpireAt == "" {
		return false
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	if file.ExpireAt < now {
		file.Delete()
		return true
	}
	return false
}
