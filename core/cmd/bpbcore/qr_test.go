package main

import "encoding/base64"

// testQrPng: a PNG holding the QR code "bpb" (generated with Python's qrcode).
func testQrPng() []byte {
	raw, err := base64.StdEncoding.DecodeString(qrPngBase64)
	if err != nil {
		panic(err)
	}
	return raw
}

const qrPngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAMgAAADIAQAAAACFI5MzAAAA90lEQVR4nO2Xuw6EMAwEZ0/8/y/vFc6DhisMOicSNMhsM6s4ayNz8XyuhFf5o4L7ARkwo3I9W0Y5CCeIZqdX9WxpRQo3aFarsOUVWWGojOBZxZrXp4bgAeUAuguDPKp6tqyf1mSyrFEtwJb0c+oxnap6tuz88Wn4uH/YeP5gZCTTxtDGeT3ujzy6TdYSbPn7I4cdZhwswJb0I6LRDMjukV3PllE0001YcZO8BlvST+uwmD7TUD3b7f26xwHCC7Cl8zqCbWRCvOvZ0n7i0eg+4RXYbvppsb33PvoBsE38ebstCVv3W9+v42Ss2ODq2W7OnyKCV/mlfAGo2luMx3OYNQAAAABJRU5ErkJggg=="
