# Installs Gantry for Windows (experimental) for this user, without admin.
# Instala o Gantry no Windows (experimental) para este usuário, sem admin.
#
#   irm https://gonservice.github.io/gantry/install.ps1 | iex
#
# The app goes to %LOCALAPPDATA%\Programs\Gantry, with a Start menu entry.
# The zip is checked against the SHA256SUMS of the release first. Running it
# again updates. $env:GANTRY_VERSION = "0.48.0" picks a version.
# Gantry on Windows uses WSL2: each distribution appears as a machine.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$repo = 'GonService/gantry'
$pt = (Get-Culture).Name -like 'pt*'
function Say([string]$p, [string]$e) { if ($pt) { Write-Host $p } else { Write-Host $e } }
function Fail([string]$p, [string]$e) { if ($pt) { throw "Erro: $p" } else { throw "Error: $e" } }

$v = $env:GANTRY_VERSION
if (-not $v) {
    try { $v = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest" -Headers @{ 'User-Agent' = 'gantry-install' }).tag_name }
    catch { Fail 'não consegui ver a versão mais nova no GitHub' 'could not find the newest version on GitHub' }
}
$v = $v.TrimStart('v')
if ($v -notmatch '^\d+\.\d+\.\d+$') { Fail "versão inválida: $v" "invalid version: $v" }

$base = "https://github.com/$repo/releases/download/v$v"
$zip = "Gantry-$v-windows.zip"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("gantry-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Say "Gantry $v" "Gantry $v"
    Say "Baixando $zip…" "Downloading $zip…"
    Invoke-WebRequest "$base/SHA256SUMS" -OutFile "$tmp\SHA256SUMS" -UseBasicParsing
    Invoke-WebRequest "$base/$zip" -OutFile "$tmp\$zip" -UseBasicParsing

    $want = (Get-Content "$tmp\SHA256SUMS" | Where-Object { $_ -match "\s\*?$([regex]::Escape($zip))$" } | ForEach-Object { ($_ -split '\s+')[0] }) | Select-Object -First 1
    if (-not $want) { Fail "$zip não está no SHA256SUMS" "$zip is not in SHA256SUMS" }
    $got = (Get-FileHash "$tmp\$zip" -Algorithm SHA256).Hash.ToLower()
    if ($got -ne $want.ToLower()) { Fail "o arquivo $zip não confere com o SHA256SUMS" "$zip does not match SHA256SUMS" }

    if (Get-Process -Name Gantry -ErrorAction SilentlyContinue) {
        Fail 'feche o Gantry e rode de novo' 'close Gantry and run this again'
    }
    $dest = Join-Path $env:LOCALAPPDATA 'Programs\Gantry'
    New-Item -ItemType Directory -Force -Path $dest | Out-Null
    Expand-Archive "$tmp\$zip" -DestinationPath $dest -Force
    $exe = Join-Path $dest 'Gantry.exe'
    Unblock-File $exe

    $menu = Join-Path ([Environment]::GetFolderPath('Programs')) 'Gantry.lnk'
    $sh = (New-Object -ComObject WScript.Shell).CreateShortcut($menu)
    $sh.TargetPath = $exe
    $sh.WorkingDirectory = $dest
    $sh.Save()

    Say "Gantry instalado em $exe, com atalho no menu Iniciar." "Gantry installed in $exe, with a Start menu entry."
    if (-not (Get-Command wsl.exe -ErrorAction SilentlyContinue)) {
        Say 'O Gantry no Windows usa o WSL2. Instale com:  wsl --install' 'Gantry on Windows uses WSL2. Install it with:  wsl --install'
    }
}
finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
