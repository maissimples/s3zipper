package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"path"

	"net/http"

	"github.com/maissimples/s3zipper/Godeps/_workspace/src/github.com/AdRoll/goamz/aws"
	"github.com/maissimples/s3zipper/Godeps/_workspace/src/github.com/AdRoll/goamz/s3"
	redigo "github.com/maissimples/s3zipper/Godeps/_workspace/src/github.com/garyburd/redigo/redis"

	newrelic "github.com/newrelic/go-agent"
)

type configuration struct {
	AccessKey          string
	SecretKey          string
	Bucket             string
	Region             string
	RedisServerAndPort string
	RedisAuth          string
	Port               string
}

type newRelicConfiguration struct {
	AppName            string
	SecretKey          string
}

var (
	config configuration
	newRelicConfig newRelicConfiguration
	awsBucket *s3.Bucket
	redisPool *redigo.Pool
	newRelicApp newrelic.Application
)

type redisFile struct {
	FileName string
	Folder   string
	S3Path   string
	// Optional - we use are Teamwork.com but feel free to rmove
	FileID       int64 `json:",string"`
	ProjectID    int64 `json:",string"`
	ProjectName  string
	Modified     string
	ModifiedTime time.Time
}

func main() {
	if 1 == 0 {
		test()
		return
	}

	initConfig()
	initNewRelicAgent()
	initAwsBucket()
	initRedis()

	fmt.Println("Running on port", config.Port)
	http.HandleFunc(newrelic.WrapHandleFunc(newRelicApp, "/", handler))
	http.ListenAndServe(":"+config.Port, nil)
}

func test() {
	var err error
	var files []*redisFile
	jsonData := "[{\"S3Path\":\"1\\/p23216.tf_A89A5199-F04D-A2DE-5824E635AC398956.Avis_Rent_A_Car_Print_Reservation.pdf\",\"FileVersionId\":\"4164\",\"FileName\":\"Avis Rent A Car_ Print Reservation.pdf\",\"ProjectName\":\"Superman\",\"ProjectID\":\"23216\",\"Folder\":\"\",\"FileID\":\"4169\"},{\"modified\":\"2015-07-18T02:05:04Z\",\"S3Path\":\"1\\/p23216.tf_351310E0-DF49-701F-60601109C2792187.a1.jpg\",\"FileVersionId\":\"4165\",\"FileName\":\"a1.jpg\",\"ProjectName\":\"Superman\",\"ProjectID\":\"23216\",\"Folder\":\"Level 1\\/Level 2 x\\/Level 3\",\"FileID\":\"4170\"}]"

	resultByte := []byte(jsonData)

	err = json.Unmarshal(resultByte, &files)
	if err != nil {
		err = errors.New("Error decoding json: " + jsonData)
	}

	parseFileDates(files)
}

func defaults(value, def string) string {
	if value == "" {
		return def
	}
	return value
}

func initConfig() {
	config = configuration{
		AccessKey:          os.Getenv("AWS_ACCESS_KEY"),
		SecretKey:          os.Getenv("AWS_SECRET_KEY"),
		Bucket:             os.Getenv("AWS_BUCKET"),
		Region:             defaults(os.Getenv("AWS_REGION"), "us-east-1"),
		RedisServerAndPort: os.Getenv("REDIS_URL"),
		RedisAuth:          os.Getenv("REDIS_AUTH"),
		Port:               defaults(os.Getenv("PORT"), "8000"),
	}
}

func initNewRelicAgent() {
	newRelicConfig = newRelicConfiguration{
		AppName:						defaults(os.Getenv("NEW_RELIC_APP_NAME"), "s3zipper-stg"),
		SecretKey:					defaults(os.Getenv("NEW_RELIC_LICENSE_KEY"), "60b00e37eb643d5a2156a668dbe2de37f93dc626"),
	}

	config := newrelic.NewConfig(newRelicConfig.AppName, newRelicConfig.SecretKey)
	config.Logger = newrelic.NewDebugLogger(os.Stdout)

	var err error
	newRelicApp, err = newrelic.NewApplication(config)
	if nil != err {
		panic(err)
	}
}

func parseFileDates(files []*redisFile) {
	layout := "2006-01-02T15:04:05Z"
	for _, file := range files {
		t, err := time.Parse(layout, file.Modified)
		if err != nil {
			fmt.Println(err)
			continue
		}
		file.ModifiedTime = t
	}
}

func initAwsBucket() {
	expiration := time.Now().Add(time.Hour * 1)
	auth, err := aws.GetAuth(config.AccessKey, config.SecretKey, "", expiration) //"" = token which isn't needed
	if err != nil {
		panic(err)
	}

	awsBucket = s3.New(auth, aws.GetRegion(config.Region)).Bucket(config.Bucket)
}

func initRedis() {
	redisPool = &redigo.Pool{
		MaxIdle:     10,
		IdleTimeout: 1 * time.Second,
		Dial: func() (redigo.Conn, error) {
			c, err := redigo.Dial("tcp", config.RedisServerAndPort)
			if err != nil {
				return nil, err
			}
			if auth := config.RedisAuth; auth != "" {
				if _, err := c.Do("AUTH", auth); err != nil {
					c.Close()
					return nil, err
				}
			}
			return c, err
		},
		TestOnBorrow: func(c redigo.Conn, t time.Time) (err error) {
			_, err = c.Do("PING")
			if err != nil {
				panic("Error connecting to redis")
			}
			return
		},
	}
}

// Remove all other unrecognised characters apart from
var makeSafeFileName = regexp.MustCompile(`[#<>:"/\|?*\\]`)

func getFilesFromRedis(ref string) (files []*redisFile, err error) {

	// Testing - enable to test. Remove later.
	if 1 == 0 && ref == "test" {
		files = append(files, &redisFile{FileName: "test.zip", Folder: "", S3Path: "test/test.zip"}) // Edit and dplicate line to test
		return
	}

	redis := redisPool.Get()
	defer redis.Close()

	// Get the value from Redis
	result, err := redis.Do("GET", "zip:"+ref)
	if err != nil || result == nil {
		err = errors.New("Access Denied (sorry your link has timed out)")
		return
	}

	// Convert to bytes
	var resultByte []byte
	var ok bool
	if resultByte, ok = result.([]byte); !ok {
		err = errors.New("Error converting data stream to bytes")
		return
	}

	// Decode JSON
	err = json.Unmarshal(resultByte, &files)
	if err != nil {
		err = errors.New("Error decoding json: " + string(resultByte))
	}

	// Convert mofified date strings to time objects
	parseFileDates(files)

	return
}

func handler(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Get "ref" URL params
	refs, ok := r.URL.Query()["ref"]
	if !ok || len(refs) < 1 {
		http.Error(w, "S3 File Zipper. Pass ?ref= to use.", 500)
		return
	}
	ref := refs[0]

	// Get "downloadas" URL params
	downloadas, ok := r.URL.Query()["downloadas"]
	if !ok && len(downloadas) > 0 {
		downloadas[0] = makeSafeFileName.ReplaceAllString(downloadas[0], "")
		if downloadas[0] == "" {
			downloadas[0] = "download.zip"
		}
	} else {
		downloadas = append(downloadas, "download.zip")
	}

	files, err := getFilesFromRedis(ref)
	if err != nil {
		http.Error(w, err.Error(), 403)
		log.Printf("%s\t%s\t%s", r.Method, r.RequestURI, err.Error())
		return
	}

	// Start processing the response
	w.Header().Add("Content-Disposition", "attachment; filename=\""+downloadas[0]+"\"")
	w.Header().Add("Content-Type", "application/zip")

	// initializing list of filenames already used
	fileNamesList := make(map[string]int)

	// Loop over files, add them to the
	zipWriter := zip.NewWriter(w)
	for _, file := range files {

		// Build safe file file name
		safeFileName := makeSafeFileName.ReplaceAllString(file.FileName, "")
		if safeFileName == "" { // Unlikely but just in case
			safeFileName = "file"
		}

		// Building another name if file already exists on zip
		base := path.Base(safeFileName)
		if fileNamesList[base] != 0 {
			extension := path.Ext(safeFileName)
			filename := base[:len(base)-len(extension)]

			safeFileName = filename + " (" + strconv.Itoa(fileNamesList[base]) + ")" + extension
			fileNamesList[base] = fileNamesList[base] + 1
		} else {
			fileNamesList[base] = 1
		}

		// Read file from S3, log any errors
		rdr, err := awsBucket.GetReader(file.S3Path)
		if err != nil {
			switch t := err.(type) {
			case *s3.Error:
				if t.StatusCode == 404 {
					log.Printf("File not found. %s", file.S3Path)
				}
			default:
				log.Printf("Error downloading \"%s\" - %s", file.S3Path, err.Error())
			}
			continue
		}

		// Build a good path for the file within the zip
		zipPath := ""
		// Prefix project Id and name, if any (remove if you don't need)
		if file.ProjectID > 0 {
			zipPath += strconv.FormatInt(file.ProjectID, 10) + "."
			// Build Safe Project Name
			file.ProjectName = makeSafeFileName.ReplaceAllString(file.ProjectName, "")
			if file.ProjectName == "" { // Unlikely but just in case
				file.ProjectName = "Project"
			}
			zipPath += file.ProjectName + "/"
		}
		// Prefix folder name, if any
		if file.Folder != "" {
			zipPath += file.Folder
			if !strings.HasSuffix(zipPath, "/") {
				zipPath += "/"
			}
		}
		zipPath += safeFileName

		// We have to set a special flag so zip files recognize utf file names
		// See http://stackoverflow.com/questions/30026083/creating-a-zip-archive-with-unicode-filenames-using-gos-archive-zip
		h := &zip.FileHeader{
			Name:   zipPath,
			Method: zip.Deflate,
			Flags:  0x800,
		}

		if file.Modified != "" {
			h.SetModTime(file.ModifiedTime)
		}

		f, _ := zipWriter.CreateHeader(h)

		io.Copy(f, rdr)
		rdr.Close()
	}

	zipWriter.Close()

	log.Printf("%s\t%s\t%s", r.Method, r.RequestURI, time.Since(start))
}
