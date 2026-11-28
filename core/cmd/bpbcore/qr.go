// QR decoding from image files (Windows has no camera; the chain-proxies page's QR scan
// becomes "decode from an image"), via the gozxing port of ZXing.
package main

import (
        "bytes"
        "encoding/base64"
        "fmt"
        "image"
        _ "image/jpeg"
        _ "image/png"

        "github.com/makiuchi-d/gozxing"
        "github.com/makiuchi-d/gozxing/qrcode"
)

// decodeQr reads a base64-encoded PNG/JPEG image and returns the QR code's text, if any.
func decodeQr(base64Image string) (string, string) {
        raw, err := base64.StdEncoding.DecodeString(base64Image)
        if err != nil {
                return "", "The image could not be read: " + err.Error()
        }
        img, _, err := image.Decode(bytes.NewReader(raw))
        if err != nil {
                return "", fmt.Sprintf("The image could not be decoded: %v", err)
        }
        bitmap, err := gozxing.NewBinaryBitmapFromImage(img)
        if err != nil {
                return "", err.Error()
        }
        hints := map[gozxing.DecodeHintType]any{}
        reader := qrcode.NewQRCodeReader()
        result, err := reader.Decode(bitmap, hints)
        if err != nil {
                return "", "No QR code was found in the image."
        }
        return result.GetText(), ""
}
