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

  redigo "github.com/garyburd/redigo/redis"
  newrelic "github.com/newrelic/go-agent"

  "github.com/aws/aws-sdk-go/aws"
  "github.com/aws/aws-sdk-go/aws/credentials"
  "github.com/aws/aws-sdk-go/aws/session"
  "github.com/aws/aws-sdk-go/service/s3"
)

type configuration struct {
  AccessKey          string
  SecretKey          string
  Bucket             string
  RedisServerAndPort string
  RedisAuth          string
  Port               string
  S3Endpoint         string
  S3Region           string
  S3ForcePathStyle   bool
}

type newRelicConfiguration struct {
  AppName   string
  SecretKey string
}

var (
  config         configuration
  newRelicConfig newRelicConfiguration
  s3Client       *s3.S3
  redisPool      *redigo.Pool
  newRelicApp    newrelic.Application
)

type redisFile struct {
  FileName     string
  Folder       string
  S3Path       string
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
  initAwsBucket()
  initRedis()

  fmt.Println("Running on port", config.Port)
  http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
    log.Printf("Request to root path: %s. Sending instructions.", r.RequestURI)
    w.Header().Set("Content-Type", "text/plain; charset=utf-8")
    w.WriteHeader(http.StatusOK)
    io.WriteString(w, "This is the s3zipper service. Use the /s3zipper endpoint with the 'ref' query parameter to create a zip file.")
  })
  http.HandleFunc("/s3zipper", handler)

  err := http.ListenAndServe(":"+config.Port, nil)
  if err != nil {
    log.Fatalf("Failed to start server: %v", err)
  }
}

func test() {
  var err error
  var files []*redisFile
  jsonData := `[{"S3Path":"1/p23216.tf_A89A5199-F04D-A2DE-5824E635AC398956.Avis_Rent_A_Car_Print_Reservation.pdf","FileVersionId":"4164","FileName":"Avis Rent A Car_ Print Reservation.pdf","ProjectName":"Superman","ProjectID":"23216","Folder":"","FileID":"4169"},{"modified":"2015-07-18T02:05:04Z","S3Path":"1/p23216.tf_351310E0-DF49-701F-60601109C2792187.a1.jpg","FileVersionId":"4165","FileName":"a1.jpg","ProjectName":"Superman","ProjectID":"23216","Folder":"Level 1/Level 2 x/Level 3","FileID":"4170"}]`

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
  forcePathStyle, _ := strconv.ParseBool(defaults(os.Getenv("S3_FORCE_PATH_STYLE"), "false"))
  config = configuration{
    AccessKey:          os.Getenv("AWS_ACCESS_KEY"),
    SecretKey:          os.Getenv("AWS_SECRET_KEY"),
    Bucket:             os.Getenv("AWS_BUCKET"),
    RedisServerAndPort: os.Getenv("REDIS_URL"),
    RedisAuth:          os.Getenv("REDIS_AUTH"),
    Port:               defaults(os.Getenv("PORT"), "8000"),
    S3Endpoint:         os.Getenv("S3_ENDPOINT"),
    S3Region:           defaults(os.Getenv("S3_REGION"), "us-east-1"),
    S3ForcePathStyle:   forcePathStyle,
  }

  if config.AccessKey == "" {
    log.Fatal("Missing required environment variable: AWS_ACCESS_KEY")
  }
  if config.SecretKey == "" {
    log.Fatal("Missing required environment variable: AWS_SECRET_KEY")
  }
  if config.Bucket == "" {
    log.Fatal("Missing required environment variable: AWS_BUCKET")
  }
  if config.RedisServerAndPort == "" {
    log.Fatal("Missing required environment variable: REDIS_URL")
  }
}

func initNewRelicAgent() {
  newRelicConfig = newRelicConfiguration{
    AppName:   defaults(os.Getenv("NEW_RELIC_APP_NAME"), "s3zipper-stg"),
    SecretKey: defaults(os.Getenv("NEW_RELIC_LICENSE_KEY"), "60b00e37eb643d5a2156a668dbe2de37f93dc626"),
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
  sess, err := session.NewSession(&aws.Config{
    Endpoint:         &config.S3Endpoint,
    Region:           &config.S3Region,
    Credentials:      credentials.NewStaticCredentials(config.AccessKey, config.SecretKey, ""),
    S3ForcePathStyle: &config.S3ForcePathStyle,
  })
  if err != nil {
    panic(err)
  }
  s3Client = s3.New(sess)
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

var makeSafeFileName = regexp.MustCompile(`[#<>:"/\|?*\\]`)

func getFilesFromRedis(ref string) (files []*redisFile, err error) {
  if 1 == 0 && ref == "test" {
    files = append(files, &redisFile{FileName: "test.zip", Folder: "", S3Path: "test/test.zip"})
    return
  }

  redis := redisPool.Get()
  defer redis.Close()

  result, err := redis.Do("GET", "zip:"+ref)
  if err != nil || result == nil {
    err = errors.New("Access Denied (sorry your link has timed out)")
    return
  }

  var resultByte []byte
  var ok bool
  if resultByte, ok = result.([]byte); !ok {
    err = errors.New("Error converting data stream to bytes")
    return
  }

  err = json.Unmarshal(resultByte, &files)
  if err != nil {
    err = errors.New("Error decoding json: " + string(resultByte))
  }

  parseFileDates(files)

  return
}

func handler(w http.ResponseWriter, r *http.Request) {
  start := time.Now()

  refs, ok := r.URL.Query()["ref"]
  if !ok || len(refs) < 1 {
    http.Error(w, "S3 File Zipper. Pass ?ref= to use.", 500)
    return
  }
  ref := refs[0]

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

  log.Printf("Found %%d files in Redis for ref '%s'", len(files), ref)
  if len(files) == 0 {
    msg := "No files found for the given reference."
    http.Error(w, msg, http.StatusNotFound)
    log.Printf("No files found for ref '%s'. Responded with 404.", ref)
    return
  }

  w.Header().Add("Content-Disposition", "attachment; filename=\""+downloadas[0]+"\"")
  w.Header().Add("Content-Type", "application/zip")

  fileNamesList := make(map[string]int)

  zipWriter := zip.NewWriter(w)
  for _, file := range files {

    log.Printf("Processing file: S3Path='%s', OutputFileName='%s'", file.S3Path, file.FileName)

    safeFileName := makeSafeFileName.ReplaceAllString(file.FileName, "")
    if safeFileName == "" {
      safeFileName = "file"
    }

    base := path.Base(safeFileName)
    if fileNamesList[base] != 0 {
      extension := path.Ext(safeFileName)
      filename := base[:len(base)-len(extension)]

      safeFileName = filename + " (" + strconv.Itoa(fileNamesList[base]) + ")" + extension
      fileNamesList[base] = fileNamesList[base] + 1
    } else {
      fileNamesList[base] = 1
    }

    input := &s3.GetObjectInput{
      Bucket: aws.String(config.Bucket),
      Key:    aws.String(file.S3Path),
    }
    result, err := s3Client.GetObject(input)
    if err != nil {
      log.Printf("Error downloading \"%s\" - %s", file.S3Path, err.Error())
      continue
    }
    defer result.Body.Close()

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

    f, err := zipWriter.CreateHeader(h)
    if err != nil {
      log.Printf("Error creating zip header for file %s: %s", zipPath, err.Error())
      continue
    }

    if _, err = io.Copy(f, result.Body); err != nil {
      log.Printf("Error writing file %s to zip: %s", file.FileName, err.Error())
      continue
    }
  }

  zipWriter.Close()

  log.Printf("%s\t%s\t%s", r.Method, r.RequestURI, time.Since(start))
}
