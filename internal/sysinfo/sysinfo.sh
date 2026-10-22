# Prints the machine's hardware and usage as key=value lines.
# Linux reads /proc; macOS uses sysctl, vm_stat and top.
set +e
os=$(uname -s)
echo "kernel=$(uname -r)"
echo "arch=$(uname -m)"
echo "hostname=$(hostname 2>/dev/null || uname -n)"
if [ "$os" = Darwin ]; then
	echo "os=macOS $(sw_vers -productVersion 2>/dev/null)"
	echo "cpu_model=$(sysctl -n machdep.cpu.brand_string 2>/dev/null)"
	echo "cpus=$(sysctl -n hw.ncpu)"
	echo "mem_total=$(sysctl -n hw.memsize)"
	ps=$(sysctl -n hw.pagesize)
	vm_stat | awk -v ps="$ps" '
		/Pages active/ {a=$3} /Pages wired/ {w=$4} /occupied by compressor/ {c=$5}
		END {gsub("\\.","",a); gsub("\\.","",w); gsub("\\.","",c); printf "mem_used=%d\n", (a+w+c)*ps}'
	top -l 2 -n 0 -s 1 2>/dev/null | awk '/CPU usage/ {idle=$7} END {sub("%","",idle); if (idle!="") printf "cpu_pct=%.1f\n", 100-idle}'
	# "{ sec = 1760000000, usec = 123 } …": the first number is the boot time.
	boot=$(sysctl -n kern.boottime | sed -n 's/^{ *sec = \([0-9][0-9]*\).*/\1/p')
	[ -n "$boot" ] && echo "uptime=$(( $(date +%s) - boot ))"
	echo "load=$(sysctl -n vm.loadavg | tr -d '{}' | awk '{print $1, $2, $3}')"
	d=/; [ -d /System/Volumes/Data ] && d=/System/Volumes/Data
	# IP on the local network: the address of the default route's interface.
	nif=$(route -n get default 2>/dev/null | awk '/interface:/ {print $2}')
	[ -n "$nif" ] && echo "ip=$(ipconfig getifaddr "$nif" 2>/dev/null)"
else
	. /etc/os-release 2>/dev/null && echo "os=$PRETTY_NAME"
	echo "cpu_model=$(grep -m1 -E 'model name|Model' /proc/cpuinfo | cut -d: -f2- | sed 's/^ *//')"
	echo "cpus=$(getconf _NPROCESSORS_ONLN 2>/dev/null || nproc)"
	awk '/^MemTotal/ {t=$2} /^MemAvailable/ {a=$2} END {printf "mem_total=%d\nmem_used=%d\n", t*1024, (t-a)*1024}' /proc/meminfo
	awk '/^SwapTotal/ {t=$2} /^SwapFree/ {f=$2} END {printf "swap_total=%d\nswap_used=%d\n", t*1024, (t-f)*1024}' /proc/meminfo
	s1=$(head -1 /proc/stat); sleep 0.5; s2=$(head -1 /proc/stat)
	echo "$s1 $s2" | awk '{t1=0; for(i=2;i<=11;i++) t1+=$i; i1=$5+$6; t2=0; for(i=13;i<=22;i++) t2+=$i; i2=$16+$17;
		dt=t2-t1; if (dt>0) printf "cpu_pct=%.1f\n", 100*(dt-(i2-i1))/dt}'
	echo "uptime=$(cut -d. -f1 /proc/uptime)"
	echo "load=$(cut -d' ' -f1-3 /proc/loadavg)"
	ip=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") {print $(i + 1); exit}}')
	[ -n "$ip" ] || ip=$(hostname -I 2>/dev/null | awk '{print $1}')
	echo "ip=$ip"
	d=/
fi
root_dev=$(df -Pk "$d" | awk 'NR==2 {print $1}')
df -Pk "$d" | awk 'NR==2 {printf "disk_path=%s\ndisk_total=%d\ndisk_used=%d\n", $6, $2*1024, $3*1024}'
for p in /var/lib/docker /var/lib/containers /var/lib/containerd; do
	[ -d "$p" ] || continue
	df -Pk "$p" | awk -v dev="$root_dev" 'NR==2 && $1 != dev {printf "disk2_path=%s\ndisk2_total=%d\ndisk2_used=%d\n", $6, $2*1024, $3*1024; exit}'
done
command -v qemu-system-x86_64 >/dev/null 2>&1 || command -v qemu-system-aarch64 >/dev/null 2>&1 && echo "qemu=1"
[ -e /dev/kvm ] && echo "kvm=1"
exit 0
