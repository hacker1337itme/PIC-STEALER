package main

import (
    "archive/zip"
    "bytes"
    "flag"
    "fmt"
    "io"
    "mime/multipart"
    "net/http"
    "os"
    "path/filepath"
    "sync"

    "github.com/h2non/filetype"
)

const cmd = "ziper_imager"

var (
    flags          *flag.FlagSet
    location       string
    outputZip      string
    telegramToken  string
    telegramChatID string
    message        string
    debug          bool
    silence        bool
    imageFiles     []string
)

func main() {
    setFlags()
    os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

func setFlags() {
    flags = flag.NewFlagSet(cmd, flag.ExitOnError)
    flags.StringVar(&location, "l", ".", "Search location")
    flags.StringVar(&outputZip, "o", "images.zip", "Output zip file")
    flags.StringVar(&telegramToken, "t", "", "Telegram bot token")
    flags.StringVar(&telegramChatID, "c", "", "Telegram chat ID")
    flags.StringVar(&message, "m", "", "Telegram Message")
    flags.BoolVar(&debug, "d", false, "Enable debug mode")
    flags.BoolVar(&silence, "silence", false, "Don't show error messages")
    flags.Usage = usage
}

func usage() {
    fmt.Fprintf(os.Stdout, "Usage: %s [OPTIONS]\n\n", cmd)
    fmt.Fprintln(os.Stdout, "OPTIONS:")
    flags.PrintDefaults()
}

func run(args []string, outStream, errStream io.Writer) int {
    flags.Parse(args[1:])
    if _, err := os.Stat(location); err != nil {
        fmt.Fprintf(outStream, "Location is an invalid value. %v\n", err)
        return 1
    }

    search(location, outStream, errStream)

    if len(imageFiles) > 0 {
        if err := zipImages(imageFiles, outputZip); err != nil {
            fmt.Fprintf(errStream, "Error while zipping images: %v\n", err)
            return 1
        }
        fmt.Fprintf(outStream, "Zipped %d images into %s\n", len(imageFiles), outputZip)

        // Check the file size before sending
        zipInfo, err := os.Stat(outputZip)
        if err != nil {
            fmt.Fprintf(errStream, "Error getting zip file stats: %v\n", err)
            return 1
        }

        if zipInfo.Size() > 20*1024*1024 { // 20 MB limit
            fmt.Fprintf(errStream, "The zip file is too large to send via Telegram (%d bytes).\n", zipInfo.Size())
            return 1
        }

        // Send a test message
        if err := sendMessageToTelegram(message, telegramToken, telegramChatID); err != nil {
            fmt.Fprintf(errStream, "Error sending test message to Telegram: %v\n", err)
            return 1
        }

        // Send zipped file to Telegram, if token and chat ID are provided
        if telegramToken != "" && telegramChatID != "" && message != "" {
            if err := sendFileToTelegram(outputZip, message ,telegramToken, telegramChatID); err != nil {
                fmt.Fprintf(errStream, "Error sending file to Telegram: %v\n", err)
                return 1
            }
            fmt.Fprintf(outStream, "Sent %s to Telegram chat %s\n", outputZip, telegramChatID)
        }
    } else {
        fmt.Fprintln(outStream, "No image files found.")
    }
    return 0
}

func search(location string, outStream, errStream io.Writer) {
    var wg sync.WaitGroup

    entries, err := os.ReadDir(location)
    if err != nil {
        fmt.Fprintf(errStream, "%v\n", err)
        return
    }

    for _, entry := range entries {
        info, err := entry.Info()
        if err != nil {
            fmt.Fprintf(errStream, "%v\n", err)
            continue
        }

        fullPath := filepath.Join(location, info.Name())
        if info.IsDir() {
            wg.Add(1)
            go func(path string) {
                defer wg.Done()
                search(path, outStream, errStream)
            }(fullPath)
        } else if isImageFile(fullPath, errStream) {
            imageFiles = append(imageFiles, fullPath)
            fmt.Fprintln(outStream, fullPath)
        }
    }
    wg.Wait()
}

func isImageFile(path string, errStream io.Writer) bool {
    file, err := os.Open(path)
    if err != nil && !silence {
        fmt.Fprintf(errStream, "file open failed %s, %v\n", path, err)
        return false
    }
    defer file.Close()

    buf := make([]byte, 261)
    if _, err := file.Read(buf); err != nil && !silence {
        fmt.Fprintf(errStream, "file read failed %s, %v\n", path, err)
        return false
    }

    isImage := filetype.IsImage(buf)
    if debug {
        if isImage {
            fmt.Println("Image file: " + path)
        } else {
            fmt.Fprintf(errStream, "Not Image file: '%s'\n", path)
        }
    }
    return isImage
}

func zipImages(imageFiles []string, output string) error {
    zipFile, err := os.Create(output)
    if err != nil {
        return err
    }
    defer zipFile.Close()

    zipWriter := zip.NewWriter(zipFile)
    defer zipWriter.Close()

    for _, img := range imageFiles {
        if err := addToZip(img, zipWriter); err != nil {
            return err
        }
    }
    return nil
}

func addToZip(imgPath string, zipWriter *zip.Writer) error {
    fileToZip, err := os.Open(imgPath)
    if err != nil {
        return err
    }
    defer fileToZip.Close()

    info, err := fileToZip.Stat()
    if err != nil {
        return err
    }

    header, err := zip.FileInfoHeader(info)
    if err != nil {
        return err
    }
    header.Name = imgPath
    header.Method = zip.Deflate

    writer, err := zipWriter.CreateHeader(header)
    if err != nil {
        return err
    }

    _, err = io.Copy(writer, fileToZip)
    return err
}

func sendFileToTelegram(filePath,message, token, chatID string) error {
    if chatID == "" {
        return fmt.Errorf("chat_id is empty")
    }

    url := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument?chat_id=%s", token, chatID)

    // Create an input file to send
    fileData, err := os.Open(filePath)
    if err != nil {
        return fmt.Errorf("error opening file: %v", err)
    }
    defer fileData.Close()

    // Prepare the form data
    var buf bytes.Buffer
    form := multipart.NewWriter(&buf)

    // Add the document
    part, err := form.CreateFormFile("document", filepath.Base(filePath))
    if err != nil {
        return fmt.Errorf("error creating form file: %v", err)
    }

    if _, err = io.Copy(part, fileData); err != nil {
        return fmt.Errorf("error copying file data: %v", err)
    }
    // Close the form
    if err := form.Close(); err != nil {
        return fmt.Errorf("error closing form: %v", err)
    }

    // Send the request
    req, err := http.NewRequest("POST", url, &buf)
    if err != nil {
        return fmt.Errorf("error creating request: %v", err)
    }
    req.Header.Set("Content-Type", form.FormDataContentType())

    // Send the request
    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return fmt.Errorf("error sending request: %v", err)
    }
    defer resp.Body.Close()

    // Read response body for debugging
    respBody, err := io.ReadAll(resp.Body)
    if err != nil {
        return fmt.Errorf("error reading response body: %v", err)
    }

    // Check response
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("failed to send message, status code: %d, response: %s", resp.StatusCode, string(respBody))
    }

    return nil
}

func sendMessageToTelegram(message, token, chatID string) error {
    if chatID == "" {
        return fmt.Errorf("chat_id is empty")
    }

    url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

    payload := fmt.Sprintf(`{"chat_id": "%s", "text": "%s"}`, chatID, message)

    req, err := http.NewRequest("POST", url, bytes.NewBuffer([]byte(payload)))
    if err != nil {
        return err
    }
    req.Header.Set("Content-Type", "application/json")

    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    // Read response body for debugging
    respBody, err := io.ReadAll(resp.Body)
    if err != nil {
        return fmt.Errorf("error reading response body: %v", err)
    }

    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("failed to send message, status code: %d, response: %s", resp.StatusCode, string(respBody))
    }

    return nil
}
