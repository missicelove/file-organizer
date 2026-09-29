package main

type catInfo struct {
	Key   string
	Name  string
	Color string
	Exts  []string
}

// 文件类型分类。顺序即界面上的默认顺序。
var categories = []catInfo{
	{"image", "图片", "#e8743b", []string{"jpg", "jpeg", "png", "gif", "bmp", "webp", "tif", "tiff", "heic", "heif", "svg", "ico", "raw", "cr2", "cr3", "nef", "arw", "dng", "orf", "rw2", "jfif", "avif"}},
	{"video", "视频", "#d64550", []string{"mp4", "mkv", "avi", "mov", "wmv", "flv", "rmvb", "rm", "m4v", "mpg", "mpeg", "3gp", "ts", "m2ts", "mts", "vob", "webm", "f4v", "asf"}},
	{"audio", "音频", "#b04fb0", []string{"mp3", "wav", "flac", "aac", "m4a", "ogg", "wma", "ape", "amr", "mid", "midi", "opus", "aiff", "aif", "dsf", "dff"}},
	{"doc", "文档", "#3d7fd6", []string{"doc", "docx", "txt", "rtf", "odt", "wps", "md", "pages", "tex", "log", "dot", "dotx", "docm"}},
	{"sheet", "表格", "#2f9e62", []string{"xls", "xlsx", "csv", "ods", "et", "numbers", "xlsm", "xlsb", "tsv"}},
	{"slide", "演示文稿", "#d98c1f", []string{"ppt", "pptx", "odp", "dps", "key", "pps", "ppsx", "pptm"}},
	{"pdf", "PDF", "#c0392b", []string{"pdf", "ofd", "xps", "caj", "djvu"}},
	{"ebook", "电子书", "#8a6d3b", []string{"epub", "mobi", "azw", "azw3", "chm", "fb2"}},
	{"archive", "压缩包/镜像", "#7b61c9", []string{"zip", "rar", "7z", "tar", "gz", "tgz", "bz2", "xz", "zst", "cab", "iso", "img", "dmg", "vhd", "vhdx", "vmdk", "gho", "wim"}},
	{"program", "程序/安装包", "#1f8a8a", []string{"exe", "msi", "apk", "bat", "cmd", "ps1", "vbs", "jar", "appx", "msix", "com", "scr", "reg", "sh", "app", "pkg", "deb", "rpm"}},
	{"code", "代码/网页", "#4a6a8a", []string{"py", "js", "ts", "jsx", "tsx", "java", "c", "cpp", "cc", "h", "hpp", "cs", "go", "rs", "php", "rb", "swift", "kt", "html", "htm", "css", "scss", "less", "json", "xml", "yml", "yaml", "sql", "vue", "ipynb", "lua", "r", "m", "pl", "toml"}},
	{"design", "设计/工程图", "#c2549b", []string{"psd", "ai", "cdr", "sketch", "fig", "xd", "dwg", "dxf", "skp", "max", "blend", "fbx", "obj", "stl", "3ds", "c4d", "indd", "eps", "prproj", "aep", "step", "stp", "igs", "sldprt", "sldasm", "prt"}},
	{"font", "字体", "#6b7a45", []string{"ttf", "otf", "ttc", "woff", "woff2", "fon", "eot"}},
	{"system", "系统/临时文件", "#8c8c8c", []string{"dll", "sys", "tmp", "temp", "bak", "old", "ini", "cfg", "conf", "dat", "db", "sqlite", "lnk", "url", "cache", "lock", "pdb", "manifest", "mui", "cat", "inf", "etl", "dmp", "crdownload", "part", "download", "ds_store", "thumbs"}},
	{"noext", "无扩展名", "#a0a0a0", nil},
	{"other", "其他", "#b5b5b5", nil},
}

var extCategory = func() map[string]string {
	m := map[string]string{}
	for _, c := range categories {
		for _, e := range c.Exts {
			if _, dup := m[e]; !dup {
				m[e] = c.Key
			}
		}
	}
	return m
}()

func categoryOf(ext string) string {
	if ext == "" {
		return "noext"
	}
	if k, ok := extCategory[ext]; ok {
		return k
	}
	return "other"
}

func categoryInfo(key string) catInfo {
	for _, c := range categories {
		if c.Key == key {
			return c
		}
	}
	return categories[len(categories)-1]
}
