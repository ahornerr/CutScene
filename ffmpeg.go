package main

import (
	"bytes"
	"errors"
	"fmt"
	ffmpeg "github.com/u2takey/ffmpeg-go"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

type Codec string

const (
	CodecH264VAAPI Codec = "h264_vaapi"
	CodecH264NVENC Codec = "h264_nvenc"
	CodecLibx264   Codec = "libx264"
)

type FfmpegParams struct {
	URL      string
	From     string
	To       string
	Filename string
	Height   int
	QP       int
	Codec    Codec
	Metadata FfmpegParamsMetadata
}

type FfmpegParamsMetadata struct {
	Title        string
	Show         string
	SeasonNumber int
	EpisodeID    int
	Year         int
}

func DoFfmpeg(params FfmpegParams) (string, error) {
	outputMetadata := map[string]string{
		"title":   params.Metadata.Title,
		"comment": params.From,
	}

	if params.Metadata.Show != "" {
		outputMetadata["show"] = params.Metadata.Show
	}
	if params.Metadata.SeasonNumber != 0 {
		outputMetadata["season_number"] = strconv.Itoa(params.Metadata.SeasonNumber)
	}
	if params.Metadata.EpisodeID != 0 {
		outputMetadata["episode_id"] = strconv.Itoa(params.Metadata.EpisodeID)
	}
	if params.Metadata.Year != 0 {
		outputMetadata["year"] = strconv.Itoa(params.Metadata.Year)
	}

	var metadataArr []string
	for k, v := range outputMetadata {
		metadataArr = append(metadataArr, fmt.Sprintf("%s=%s", k, v))
	}

	tmpFile := filepath.Join("/tmp", params.Filename)

	inputArgs := ffmpeg.KwArgs{
		"ss":      params.From,
		"to":      params.To,
		"hwaccel": "auto",
		// TODO: Make these two configurable, we don't want them when trying to troubleshoot
		"hide_banner": "",
		"loglevel":    "error",
	}

	switch params.Codec {
	case CodecH264VAAPI:
		inputArgs["hwaccel"] = "vaapi"
		inputArgs["hwaccel_device"] = "/dev/dri/renderD128"
		inputArgs["hwaccel_output_format"] = "vaapi"
	case CodecH264NVENC:
		inputArgs["hwaccel"] = "cuda"
		inputArgs["extra_hw_frames"] = 8
	case CodecLibx264:
		fallthrough
	default:
	}

	// TODO: Might be a good idea to make these configurable or add support for presets
	outputArgs := ffmpeg.KwArgs{
		"acodec":       "libvorbis",
		"map_chapters": -1,
		"map_metadata": 0,
		"movflags":     "+use_metadata_tags+faststart",
		"metadata":     metadataArr,
		"qp":           params.QP,
	}

	outputArgs["vcodec"] = params.Codec

	switch params.Codec {
	case CodecH264VAAPI:
		outputArgs["vf"] = "hwupload,scale_vaapi=format=nv12,scale_vaapi=-2:" + strconv.Itoa(params.Height)
		outputArgs["compression_level"] = "0" // https://trac.ffmpeg.org/wiki/Hardware/VAAPI#AMDMesa
	case CodecH264NVENC:
		inputArgs["hwaccel_output_format"] = "cuda"
		if params.Height > 0 {
			outputArgs["vf"] = "scale_cuda=-2:" + strconv.Itoa(params.Height)
		}
		if params.QP == 0 {
			outputArgs["rc"] = "constqp"
			outputArgs["qp"] = 24
			outputArgs["b:v"] = "0K"
		}
	case CodecLibx264:
		fallthrough
	default:
		outputArgs["vf"] = "scale=-2:" + strconv.Itoa(params.Height)
		outputArgs["pix_fmt"] = "yuv420p"
		outputArgs["crf"] = 23
		outputArgs["video_bitrate"] = 0
		// TODO: I'm not sure if this does anything useful
		outputArgs["tune"] = "film"
	}

	errBuff := &bytes.Buffer{}
	err := ffmpeg.
		Input(params.URL, inputArgs).
		Output(tmpFile, outputArgs).
		OverWriteOutput().
		WithErrorOutput(errBuff).
		WithOutput(os.Stdout).
		Run()

	// Capture the ffmpeg process stderr if it exits unsuccessfully
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		err = fmt.Errorf("ffmpeg exited with error:\n%s", errBuff.String())
	}

	_, _ = io.Copy(os.Stderr, errBuff)

	return tmpFile, err
}

func DoFfmpegPreview(fileURL, from, to string, codec Codec, writer io.Writer) error {
	inputArgs := ffmpeg.KwArgs{
		"ss":      from,
		"to":      to,
		"hwaccel": "auto",
		// TODO: Make these two configurable, we don't want them when trying to troubleshoot
		"hide_banner": "",
		"loglevel":    "error",
	}

	switch codec {
	case CodecH264VAAPI:
		inputArgs["hwaccel"] = "vaapi"
		inputArgs["hwaccel_device"] = "/dev/dri/renderD128"
		inputArgs["hwaccel_output_format"] = "vaapi"
	case CodecH264NVENC:
		inputArgs["hwaccel"] = "cuda"
	case CodecLibx264:
		fallthrough
	default:
	}

	outputArgs := ffmpeg.KwArgs{
		"acodec":   "libvorbis",
		"f":        "mp4",
		"movflags": "frag_keyframe+empty_moov",
		// TODO
		//"qp":           params.QP,
	}

	outputArgs["vcodec"] = codec

	height := 720

	switch codec {
	case CodecH264VAAPI:
		outputArgs["vf"] = "hwupload,scale_vaapi=format=nv12,scale_vaapi=-2:" + strconv.Itoa(height)
		outputArgs["compression_level"] = "0" // https://trac.ffmpeg.org/wiki/Hardware/VAAPI#AMDMesa
	case CodecH264NVENC:
		inputArgs["hwaccel_output_format"] = "cuda"
		outputArgs["vf"] = "scale_cuda=-2:" + strconv.Itoa(height)
	case CodecLibx264:
		fallthrough
	default:
		outputArgs["vf"] = "scale=-2:" + strconv.Itoa(height)
		outputArgs["pix_fmt"] = "yuv420p"
		outputArgs["crf"] = 23
		outputArgs["video_bitrate"] = 0
		// TODO: I'm not sure if this does anything useful
		outputArgs["tune"] = "film"
	}

	errBuff := &bytes.Buffer{}
	err := ffmpeg.
		Input(fileURL, inputArgs).
		Output("pipe:", outputArgs).
		WithOutput(writer, errBuff).
		Run()

	// Capture the ffmpeg process stderr if it exits unsuccessfully
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		err = fmt.Errorf("ffmpeg exited with error:\n%s", errBuff.String())
	}

	_, _ = io.Copy(os.Stderr, errBuff)

	return err
}
