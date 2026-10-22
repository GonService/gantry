package composex

import (
	"path"
	"regexp"
	"strings"
)

// ClassifyImage tells which catalog image a file is by its name (as Cisco,
// CML, GNS3 and EVE-NG name them), "" when it is none of them:
//
//	i86bi_LinuxL2-AdvEnterpriseK9-M_152_May_2018.bin -> cisco-iol-l2
//	x86_64_crb_linux-adventerprisek9-ms.iol          -> cisco-iol
//	c7200-adventerprisek9-mz.152-4.S6.image          -> cisco-c7200
//	vios_l2-adventerprisek9-m.vmdk.SSA.152-4.0.55.E  -> cisco-iosvl2
//	iourc                                            -> cisco-iourc
func ClassifyImage(file string) string {
	n := strings.ToLower(path.Base(file))
	eveIOL := strings.Contains(strings.ToLower(file), "/iol/bin/") // EVE-NG/PNetLab keep IOL there
	switch {
	case n == "iourc" || strings.HasPrefix(n, "iourc") || n == ".iourc":
		return "cisco-iourc"
	case eveIOL || strings.Contains(n, "i86bi") || strings.Contains(n, "crb_linux") || iolName.MatchString(n):
		if l2Name.MatchString(n) {
			return "cisco-iol-l2"
		}
		return "cisco-iol"
	case strings.HasPrefix(n, "c7200"):
		return "cisco-c7200"
	case strings.HasPrefix(n, "c3725"):
		return "cisco-c3725"
	case strings.HasPrefix(n, "c2691"):
		return "cisco-c2691"
	case strings.HasPrefix(n, "vios_l2") || strings.HasPrefix(n, "viosl2") || strings.HasPrefix(n, "vios-l2"):
		return "cisco-iosvl2"
	case strings.HasPrefix(n, "vios"):
		return "cisco-iosv"
	case strings.HasPrefix(n, "csr1000v"):
		return "cisco-csr1000v"
	case strings.HasPrefix(n, "nixos-lab"):
		return "nixos-lab-26.05"
	}
	return ""
}

var (
	iolName = regexp.MustCompile(`(^|[^a-z])(iol|ioll2|iou)([^a-z]|$)`)
	l2Name  = regexp.MustCompile(`l2([^a-z0-9]|$)|linuxl2|_l2|ioll2|-l2`)
)

// IsImagePath tells whether image: is the path of a file on the machine
// (/opt/iol/L3.bin, ~/imagens/r.bin) rather than a name to look up.
func IsImagePath(image string) bool {
	return strings.HasPrefix(image, "/") || strings.HasPrefix(image, "~/")
}

// IsImageFileRef tells whether image: names a file of the machine's image
// folders (a name with the extension of an image, no folder): Gantry finds
// it there.
func IsImageFileRef(image string) bool {
	if _, catalog := Appliances[image]; catalog || IsNixOSImage(image) || image == "" || strings.ContainsAny(image, "/\\") || strings.Contains(image, "://") {
		return false
	}
	l := strings.ToLower(image)
	for _, ext := range []string{".qcow2", ".img", ".bin", ".image", ".iso", ".vmdk", ".iol"} {
		if strings.HasSuffix(l, ext) {
			return true
		}
	}
	return ClassifyImage(image) != "" && ClassifyImage(image) != "cisco-iourc"
}
