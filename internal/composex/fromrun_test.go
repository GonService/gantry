package composex

import (
	"strings"
	"testing"
)

func TestFromCommands(t *testing.T) {
	in := `sudo docker run -d --name web -p 8080:80 -e TZ=America/Sao_Paulo -e TOKEN \
  -v dados:/data -v ./conf:/etc/app:ro --network rede --restart unless-stopped \
  --memory 512m --cpus 1.5 --cap-add NET_ADMIN -it nginx:alpine nginx -g "daemon off;"
podman run --rm --network host -v /srv:/srv docker.io/library/redis:7 redis-server --appendonly yes
nerdctl --namespace k8s.io run -d -p8081:80 --health-cmd "curl -f localhost" --mount type=bind,src=/a,dst=/b busybox`
	c, err := FromCommands(in)
	if err != nil {
		t.Fatal(err)
	}
	if c.Engine != "docker" || strings.Join(c.Services, ",") != "web,redis,busybox" {
		t.Errorf("%+v", c)
	}
	for _, want := range []string{
		"  web:\n    image: nginx:alpine\n", `command:`, `- daemon off;`, "container_name: web", "restart: unless-stopped",
		"tty: true", "stdin_open: true", `- 8080:80`, `- TZ=America/Sao_Paulo`, `- TOKEN=${TOKEN}`, `- dados:/data`,
		`- ./conf:/etc/app:ro`, "networks:\n      - rede", "mem_limit: 512m", "cpus: \"1.5\"", "cap_add:",
		"network_mode: host", `- 8081:80`, "healthcheck:", "volumes:\n  dados: {}", "networks:\n  rede: {}",
	} {
		if !strings.Contains(c.YAML, want) {
			t.Errorf("faltou %q em\n%s", want, c.YAML)
		}
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "--mount") {
		t.Errorf("avisos %v", c.Warnings)
	}
	p, err := Parse([]byte(c.YAML))
	if err != nil || len(p.Names) != 3 {
		t.Fatalf("o resultado não é um compose válido: %v\n%s", err, c.YAML)
	}
	if _, err := FromCommands("ls -la"); err == nil {
		t.Error("aceitou um comando que não é run")
	}
	if c, _ := FromCommands("docker run nginx\ndocker run nginx"); strings.Join(c.Services, ",") != "nginx,nginx-2" {
		t.Errorf("nomes repetidos: %v", c.Services)
	}
}
