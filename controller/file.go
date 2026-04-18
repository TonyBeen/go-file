package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"go-file/common"
	"go-file/model"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type FileDeleteRequest struct {
	Id   int
	Link string
	//Token string
}

func parseExpireDuration(expire string) (time.Duration, error) {
	if expire == "" {
		return 0, nil
	}
	if len(expire) < 2 {
		return 0, fmt.Errorf("invalid expire format: %s", expire)
	}
	unit := expire[len(expire)-1:]
	value := expire[:len(expire)-1]
	d, err := time.ParseDuration(value + unit)
	if err == nil {
		return d, nil
	}
	// Support 'd' for days
	if unit == "d" {
		days, err := time.ParseDuration(value + "h")
		if err != nil {
			return 0, fmt.Errorf("invalid expire format: %s", expire)
		}
		return days * 24, nil
	}
	return 0, fmt.Errorf("invalid expire format: %s", expire)
}

func UploadFile(c *gin.Context) {
	// Pre-check: reject early if Content-Length exceeds limit
	if common.MaxUploadSizeBytes > 0 {
		contentLength := c.Request.ContentLength
		if contentLength > 0 {
			dbUsage := model.GetTotalFileSize()
			diskUsage := common.CurrentDiskUsage
			currentUsage := dbUsage
			if diskUsage > currentUsage {
				currentUsage = diskUsage
			}
			if currentUsage+contentLength > common.MaxUploadSizeBytes {
				c.JSON(http.StatusInsufficientStorage, gin.H{
					"success":       false,
					"message":       fmt.Sprintf("disk usage %s exceeds limit %s", common.Bytes2Size(currentUsage), common.Bytes2Size(common.MaxUploadSizeBytes)),
					"current_usage": currentUsage,
					"max_size":      common.MaxUploadSizeBytes,
				})
				return
			}
		}
	}

	uploadPath := common.UploadPath
	saveToDatabase := true
	path := c.PostForm("path")
	if path != "" { // Upload to explorer's path
		uploadPath = filepath.Join(common.ExplorerRootPath, path)
		if !strings.HasPrefix(uploadPath, common.ExplorerRootPath) {
			// In this case the given path is not valid, so we reset it to ExplorerRootPath.
			uploadPath = common.ExplorerRootPath
		}
		saveToDatabase = false

		// Start a go routine to delete explorer' cache
		if common.ExplorerCacheEnabled {
			go func() {
				ctx := context.Background()
				rdb := common.RDB
				key := "cacheExplorer:" + uploadPath
				rdb.Del(ctx, key)
			}()
		}
	}

	description := c.PostForm("description")
	expireStr := c.PostForm("expire")
	var expireAt string
	if expireStr != "" {
		d, err := parseExpireDuration(expireStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"message": "invalid expire format, examples: 30s, 5m, 1h, 2d",
			})
			return
		}
		expireAt = time.Now().Add(d).Format("2006-01-02 15:04:05")
	}
	uploader := c.GetString("username")
	if uploader == "" {
		uploader = "匿名用户"
	}
	currentTime := time.Now().Format("2006-01-02 15:04:05")
	form, err := c.MultipartForm()
	if err != nil {
		c.String(http.StatusBadRequest, fmt.Sprintf("get form err: %s", err.Error()))
		return
	}
	files := form.File["file"]
	createTextFile := false
	if files == nil && description != "" {
		createTextFile = true
		file := &multipart.FileHeader{
			Filename: "text.txt",
			Header:   nil,
			Size:     0,
		}
		files = append(files, file)
	}
	// Check disk usage limit before any disk write (DB sum first, then disk scan as fallback)
	if common.MaxUploadSizeBytes > 0 {
		dbUsage := model.GetTotalFileSize()
		diskUsage := common.CurrentDiskUsage
		currentUsage := dbUsage
		if diskUsage > currentUsage {
			currentUsage = diskUsage
		}
		var uploadSize int64
		for _, f := range files {
			uploadSize += f.Size
		}
		if currentUsage+uploadSize > common.MaxUploadSizeBytes {
			message := fmt.Sprintf("disk usage %s exceeds limit %s", common.Bytes2Size(currentUsage), common.Bytes2Size(common.MaxUploadSizeBytes))
			c.JSON(http.StatusInsufficientStorage, gin.H{
				"success":       false,
				"message":       message,
				"current_usage": currentUsage,
				"max_size":      common.MaxUploadSizeBytes,
			})
			return
		}
	}
	t := time.Now()
	subfolder := t.Format("2006-01")
	err = common.MakeDirIfNotExist(filepath.Join(uploadPath, subfolder))
	if err != nil {
		common.SysError("failed to create folder: " + err.Error())
		c.Status(http.StatusInternalServerError)
		return
	}

	var uploadErrors []string
	for _, file := range files {
		// In case someone wants to upload to other folders.
		filename := filepath.Base(file.Filename)
		link := fmt.Sprintf("%s/%s", subfolder, filename)
		savePath := filepath.Join(uploadPath, subfolder, filename)
		if _, err := os.Stat(savePath); err == nil {
			// File already existed.
			timestamp := t.Format("_2006-01-02_15-04-05")
			ext := filepath.Ext(filename)
			if ext == "" {
				link += timestamp
			} else {
				link = subfolder + "/" + filename[:len(filename)-len(ext)] + timestamp + ext
			}
			savePath = filepath.Join(uploadPath, link)
		}
		if createTextFile {
			// Create a new text file and then write the description to it.
			filename = "文本分享"
			f, err := os.Create(savePath)
			if err != nil {
				message := "failed to create file: " + err.Error()
				common.SysError(message)
				uploadErrors = append(uploadErrors, message)
				continue
			}
			_, err = f.WriteString(description)
			if err != nil {
				message := "failed to write text to file: " + err.Error()
				common.SysError(message)
				uploadErrors = append(uploadErrors, message)
				continue
			}
			descriptionRune := []rune(description)
			if len(descriptionRune) > common.AbstractTextLength {
				description = fmt.Sprintf("内容摘要：%s...", string(descriptionRune[:common.AbstractTextLength]))
			}
		} else {
			if err := c.SaveUploadedFile(file, savePath); err != nil {
				message := "failed to save uploaded file: " + err.Error()
				common.SysError(message)
				uploadErrors = append(uploadErrors, message)
				continue
			}
		}
		if saveToDatabase {
			fileObj := &model.File{
				Description: description,
				Uploader:    uploader,
				Time:        currentTime,
				Link:        link,
				Filename:    filename,
				ExpireAt:    expireAt,
				Size:        file.Size,
			}
			err = fileObj.Insert()
			if err != nil {
				common.SysError("failed to insert file to database: " + err.Error())
				uploadErrors = append(uploadErrors, err.Error())
				continue
			}
		}
	}

	// If the request is from API (Accept: application/json), return JSON with download URLs
	if c.GetHeader("Accept") == "application/json" {
		if len(uploadErrors) > 0 {
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"message": strings.Join(uploadErrors, "; "),
			})
			return
		}
		var downloadURLs []string
		for _, file := range files {
			filename := filepath.Base(file.Filename)
			link := ""
			// Find the actual link from database if saved
			if saveToDatabase {
				var fileObj model.File
				model.DB.Where("filename = ? AND uploader = ?", filename, uploader).Order("id desc").First(&fileObj)
				if fileObj.Link != "" {
					link = fileObj.Link
				}
			}
			if link == "" {
				// For explorer uploads or fallback
				link = filename
			}
			scheme := "http"
			if proto := c.GetHeader("X-Forwarded-Proto"); proto != "" {
				scheme = proto
			} else if c.Request.TLS != nil {
				scheme = "https"
			}
			host := c.Request.Host
			if port := c.GetHeader("X-Forwarded-Port"); port != "" {
				// Replace port in host
				if idx := strings.LastIndex(host, ":"); idx != -1 {
					host = host[:idx] + ":" + port
				} else {
					host = host + ":" + port
				}
			}
			downloadURL := fmt.Sprintf("%s://%s/upload/%s", scheme, host, link)
			downloadURLs = append(downloadURLs, downloadURL)
		}
		resp := gin.H{
			"success":       true,
			"message":       "OK",
			"download_urls": downloadURLs,
		}
		if expireAt != "" {
			resp["expire_at"] = expireAt
		}
		c.JSON(http.StatusOK, resp)
		return
	}
	c.Redirect(http.StatusSeeOther, "./")
}

func DeleteFile(c *gin.Context) {
	var deleteRequest FileDeleteRequest
	err := json.NewDecoder(c.Request.Body).Decode(&deleteRequest)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "invalid parameters",
		})
		return
	}

	fileObj := &model.File{
		Id: deleteRequest.Id,
	}
	model.DB.Where("id = ?", deleteRequest.Id).First(&fileObj)
	err = fileObj.Delete()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": err.Error(),
		})
	} else {
		message := "file deleted successfully"
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": message,
		})
	}

}

func DownloadFile(c *gin.Context) {
	path := c.Param("filepath")
	subfolder, filename := filepath.Split(path)
	link := filename // Keep compatibility with old version
	if subfolder != "/" {
		link = fmt.Sprintf("%s%s", subfolder, filename)
		link = strings.TrimPrefix(link, "/")
	}
	fullPath := filepath.Join(common.UploadPath, subfolder, filename)
	if !strings.HasPrefix(fullPath, common.UploadPath) {
		// We may being attacked!
		c.Status(403)
		return
	}
	// Check if file has expired
	var fileObj model.File
	model.DB.Where("link = ?", link).First(&fileObj)
	if fileObj.Id != 0 && fileObj.IsExpired() {
		c.Status(404)
		return
	}
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		c.Status(404)
		return
	}
	if strings.HasSuffix(fullPath, ".txt") && common.IsMobileUserAgent(c.Request.UserAgent()) {
		content, err := os.ReadFile(fullPath)
		if err != nil {
			c.Status(404)
			return
		}
		c.HTML(http.StatusOK, "text-copy.html", gin.H{
			"content": string(content),
		})
	} else {
		c.File(fullPath)
	}
	// Update download counter
	go func() {
		model.UpdateDownloadCounter(link)
	}()
}
