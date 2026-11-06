<#
.SYNOPSIS
在 Windows 电脑上安装 trainMaster 训练节点。

.DESCRIPTION
检测 NVIDIA 显卡与驱动 → 选择 PyTorch 变体（CUDA 版本）→ 创建虚拟环境 → 安装 PyTorch、Ultralytics 与训练节点
→ 写入配置文件 → 自检 → 可选注册为开机自启的计划任务（以 SYSTEM 运行，不需要登录）。

可以重复运行：已有的虚拟环境会复用，PyTorch 变体变化时自动重新安装；配置文件中的平台地址、令牌、节点名称、
目录与其它手动添加的设置沿用；本安装目录的计划任务已注册时，即使不加 -Service 也会更新并重启它。

需要以管理员身份运行（右键“开始”→“终端（管理员）”或“Windows PowerShell（管理员）”）。
安装目录、状态目录与配置文件只允许 SYSTEM 与管理员修改（计划任务以 SYSTEM 运行其中的程序）。

从平台直接运行（平台附带了安装脚本时，“计算资源 → 接入节点”中有完整的命令）：
  & ([scriptblock]::Create((irm 'http://192.168.1.10:8080/api/v1/compute/windows/install.ps1'))) -ServerUrl 'http://192.168.1.10:8080' -Token '<令牌>' -InstallPython -Service

在 trainMaster 仓库目录中运行：
  powershell -ExecutionPolicy Bypass -File trainer\scripts\install.ps1 -ServerUrl 'http://192.168.1.10:8080' -Token '<令牌>' -InstallPython -Service

之后更换 PyTorch 变体：同样的命令只加 -Variant cu128（其它设置沿用配置文件）。

环境变量：
  PIP_INDEX_URL      PyPI 镜像（如 https://mirrors.aliyun.com/pypi/simple）
  TORCH_INDEX_URL    PyTorch 官方索引的基础地址，默认 https://download.pytorch.org/whl（实际使用 <地址>/<变体>）
  TORCH_FIND_LINKS   扁平目录形式的 PyTorch 镜像（如 https://mirrors.aliyun.com/pytorch-wheels），设置后代替 TORCH_INDEX_URL
  TM_NVIDIA_SMI      检测显卡使用的 nvidia-smi（默认在 PATH、System32 与 NVSMI 目录中查找）
  TM_PYTHON_INSTALLER_URL / TM_VC_REDIST_URL  -InstallPython 在没有 winget 时下载的 Python / VC++ 运行库安装程序

.PARAMETER ServerUrl
平台地址，例如 http://192.168.1.10:8080（首次安装必填）。

.PARAMETER Token
节点令牌（平台 .env 中的 TM_NODE_TOKEN；平台未设置令牌时可省略）。

.PARAMETER Name
节点名称（默认为计算机名）。

.PARAMETER Variant
手动指定 PyTorch 变体：cu130、cu126、cu128、cu118、cpu（默认按驱动与显卡自动选择）。

.PARAMETER Prefix
安装目录，即虚拟环境（默认 C:\ProgramData\trainMaster\trainer）。

.PARAMETER Python
创建虚拟环境使用的 Python（64 位，3.10–3.13；默认自动查找）。

.PARAMETER StateDir
状态目录：节点标识、心跳与日志（默认 C:\ProgramData\trainMaster\state）。

.PARAMETER DataDir
数据目录：上报其所在磁盘的空间（默认为状态目录）。

.PARAMETER EnvFile
配置文件（默认为已注册的计划任务使用的配置文件，没有时为 <Prefix>\trainer.env；只有 SYSTEM 与管理员可读）。

.PARAMETER Service
注册并启动计划任务 trainmaster-trainer：开机时以 SYSTEM 运行，没有在运行时每 5 分钟自动重新启动。

.PARAMETER InstallPython
没有合适的 Python 时安装 Python 3.12，缺少 Microsoft Visual C++ 运行库时安装它（优先用 winget）。

.PARAMETER DryRun
只打印检测结果与将要执行的步骤，不做任何修改。

.PARAMETER Uninstall
停止并删除计划任务（保留安装目录、配置文件与状态目录）。

.PARAMETER Package
训练节点安装包：trainer 目录或 zip（默认为本脚本所在的 trainer 目录，没有时从平台下载）。
#>
[CmdletBinding()]
param(
    [string]$ServerUrl,
    [string]$Token,
    [string]$Name,
    [string]$Variant,
    [string]$Prefix,
    [string]$Python,
    [string]$StateDir,
    [string]$DataDir,
    [string]$EnvFile,
    [switch]$Service,
    [switch]$InstallPython,
    [switch]$DryRun,
    [switch]$Uninstall,
    [string]$Package,
    [switch]$Help
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'  # Windows PowerShell 5.1 显示下载进度时非常慢

# 用 -File 运行时为本脚本的路径；从平台用 [scriptblock]::Create 运行时为 $null。不用 $PSScriptRoot 与
# $PSCommandPath：在脚本块中它们是调用方的值（例如在另一个脚本中运行平台给出的命令时）。
$ScriptPath = if ($MyInvocation.MyCommand.CommandType -eq 'ExternalScript') { $MyInvocation.MyCommand.Path } else { $null }
# 在函数中 $PSBoundParameters 是函数自己的参数，这里先记下
$TokenGiven = $PSBoundParameters.ContainsKey('Token')
$StateDirGiven = $PSBoundParameters.ContainsKey('StateDir')

$TaskName = 'trainmaster-trainer'
$PythonVersionMin = [version]'3.10'
$PythonVersionMax = [version]'3.13'
$PythonWingetId = 'Python.Python.3.12'
$PythonDefaultDir = 'Python312'
$PythonInstallerUrl = 'https://www.python.org/ftp/python/3.12.10/python-3.12.10-amd64.exe'
$VcRedistUrl = 'https://aka.ms/vs/17/release/vc_redist.x64.exe'
$DefaultTorchIndex = 'https://download.pytorch.org/whl'
# 本脚本管理的配置项（配置文件中其余的设置原样保留）
$ManagedKeys = @('TM_SERVER_URL', 'TM_NODE_TOKEN', 'TM_NODE_NAME', 'TM_TORCH_VARIANT', 'TM_STATE_DIR',
    'TM_DATA_DIR', 'TM_LOG_FILE', 'CUDA_DEVICE_ORDER')
# 账户的 SID（不受系统语言影响）
$SidSystem = 'S-1-5-18'
$SidAdmins = 'S-1-5-32-544'
$SidUsers = 'S-1-5-32-545'
$SidTrustedInstaller = 'S-1-5-80-956008885-3418522649-1831038044-1850103602-2229827870'
$SidOwnerRights = 'S-1-3-4'
# 可变的状态放在哈希表中：函数通过作用域链读取它（在 [scriptblock]::Create 运行时没有 $script: 作用域）
$Ctx = @{ ExitCode = 0; TempDirs = @(); ConsoleEncoding = $null; PythonEnv = $null; ModulePath = $null }

# ---- 输出与错误 ----

function Write-Info([string]$Message) { Write-Host "install: $Message" }
function Write-Warn([string]$Message) { Write-Host "install: 注意：$Message" -ForegroundColor Yellow }

# 结束安装（打印原因）。从平台用 [scriptblock]::Create 运行时不能用 exit：那会关闭用户的 PowerShell 窗口。
function Stop-Install([int]$Code, [string]$Message) {
    $Ctx.ExitCode = $Code
    throw "TM_INSTALL_ABORT:$Message"
}

function Format-Arg([string]$Value) {
    if ($Value -eq '') { return '""' }
    if ($Value -match '[\s"]') { return '"' + ($Value -replace '"', '\"') + '"' }
    return $Value
}

function Write-Command([string]$FilePath, [string[]]$Arguments) {
    $parts = @(Format-Arg $FilePath) + @($Arguments | ForEach-Object { Format-Arg $_ })
    Write-Host ('+ ' + ($parts -join ' '))
}

# 运行外部命令并显示其输出（-DryRun 时只打印）。原生命令写到 stderr 的内容不当作 PowerShell 错误
# （Windows PowerShell 5.1 在 $ErrorActionPreference = 'Stop' 时会因为 pip 的警告而中止）。
function Invoke-Step {
    param([string]$FilePath, [string[]]$Arguments = @(), [switch]$AllowFailure, [string]$Failure)
    Write-Command $FilePath $Arguments
    if ($DryRun) { return 0 }
    $old = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $global:LASTEXITCODE = -1  # 命令无法启动时不会设置 $LASTEXITCODE
    try {
        & $FilePath @Arguments | Out-Host
        $code = $LASTEXITCODE
    } catch {
        Write-Host $_.Exception.Message
        $code = -1
    } finally {
        $ErrorActionPreference = $old
    }
    if ($code -ne 0 -and -not $AllowFailure) {
        if (-not $Failure) { $Failure = "命令失败（退出码 $code）：$FilePath $($Arguments -join ' ')" }
        Stop-Install 1 $Failure
    }
    return $code
}

# 按 Windows 命令行的规则（MS C 运行库）拼接参数：含空白或双引号的参数加双引号，双引号前的反斜杠加倍。
function ConvertTo-ArgString([string[]]$Arguments) {
    $parts = foreach ($a in $Arguments) {
        if ($a -ne '' -and $a -notmatch '[\s"]') { $a; continue }
        $sb = New-Object System.Text.StringBuilder
        [void]$sb.Append('"')
        $slashes = 0
        foreach ($ch in $a.ToCharArray()) {
            if ($ch -eq '\') { $slashes += 1; continue }
            if ($ch -eq '"') { [void]$sb.Append('\' * ($slashes * 2 + 1)).Append('"'); $slashes = 0; continue }
            if ($slashes -gt 0) { [void]$sb.Append('\' * $slashes); $slashes = 0 }
            [void]$sb.Append($ch)
        }
        [void]$sb.Append('\' * ($slashes * 2)).Append('"')
        $sb.ToString()
    }
    return ($parts -join ' ')
}

# 运行外部命令（不显示，不受 -DryRun 影响：只用于检测），按 UTF-8 读取标准输出与标准错误。
# 返回 @{ ExitCode; Stdout; Stderr }；无法启动时为 $null。Python 以 UTF-8 输出（PYTHONUTF8、PYTHONIOENCODING）。
function Invoke-Capture([string]$FilePath, [string[]]$Arguments = @(), [hashtable]$Environment = @{}) {
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $FilePath
    $psi.Arguments = ConvertTo-ArgString $Arguments
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.StandardOutputEncoding = New-Object System.Text.UTF8Encoding $false
    $psi.StandardErrorEncoding = New-Object System.Text.UTF8Encoding $false
    $psi.EnvironmentVariables['PYTHONUTF8'] = '1'
    $psi.EnvironmentVariables['PYTHONIOENCODING'] = 'utf-8'
    foreach ($key in $Environment.Keys) { $psi.EnvironmentVariables[$key] = $Environment[$key] }
    try {
        $proc = [System.Diagnostics.Process]::Start($psi)
    } catch {
        return $null
    }
    $stderr = $proc.StandardError.ReadToEndAsync()
    $stdout = $proc.StandardOutput.ReadToEnd()
    $proc.WaitForExit()
    return @{ ExitCode = $proc.ExitCode; Stdout = $stdout; Stderr = $stderr.Result }
}

# 标准错误的最后几行（错误信息通常在最后）。
function Get-Tail([string]$Text, [int]$Count = 6) {
    $lines = @(($Text -split "`r?`n") | Where-Object { $_.Trim() -ne '' })
    if ($lines.Count -gt $Count) { $lines = $lines[($lines.Count - $Count)..($lines.Count - 1)] }
    return ($lines -join "`n")
}

# ---- 配置文件（与 trainmaster_trainer/envfile.py 相同的格式）----

# 一行中 = 之后的部分 → 值；格式不对时抛出异常（中文说明）。
function ConvertFrom-EnvValue([string]$Raw) {
    $text = $Raw.Trim()
    if ($text -eq '' -or ($text[0] -ne "'" -and $text[0] -ne '"')) { return $text }
    $quote = $text[0]
    $sb = New-Object System.Text.StringBuilder
    $i = 1
    while ($i -lt $text.Length) {
        $c = $text[$i]
        if ($c -eq $quote) {
            $rest = $text.Substring($i + 1).Trim()
            if ($rest -ne '' -and -not $rest.StartsWith('#')) { throw "引号后面还有内容：$rest" }
            return $sb.ToString()
        }
        if ($quote -eq '"' -and $c -eq '\' -and $i + 1 -lt $text.Length -and '\"$`'.Contains([string]$text[$i + 1])) {
            [void]$sb.Append($text[$i + 1])
            $i += 2
            continue
        }
        [void]$sb.Append($c)
        $i += 1
    }
    throw '缺少结尾的引号'
}

# 读取配置文件：返回 @{ Settings = 本脚本管理的配置项; Extra = 其它设置的原始行 }。
# （不用 Values、Keys 这样的键名：它们与哈希表自己的属性同名。）
function Read-EnvFile([string]$Path) {
    $settings = @{}
    $extra = New-Object System.Collections.Generic.List[string]
    $text = [IO.File]::ReadAllText($Path, [Text.Encoding]::UTF8)
    $lineno = 0
    foreach ($line in ($text.TrimStart([char]0xFEFF) -split "`r?`n")) {
        $lineno += 1
        $stripped = $line.Trim()
        if ($stripped -eq '' -or $stripped.StartsWith('#')) { continue }
        if ($stripped -cmatch '^export[ \t]') { $stripped = $stripped.Substring(6).TrimStart() }
        $eq = $stripped.IndexOf('=')
        $key = if ($eq -ge 0) { $stripped.Substring(0, $eq).Trim() } else { '' }
        if ($key -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') {
            Stop-Install 1 "配置文件 $Path 第 $lineno 行不是 KEY=VALUE 的格式，请修改后重新运行"
        }
        if ($ManagedKeys -contains $key) {
            try {
                $settings[$key] = ConvertFrom-EnvValue $stripped.Substring($eq + 1)
            } catch {
                Stop-Install 1 "配置文件 $Path 第 $lineno 行（$key）：$($_.Exception.Message)"
            }
        } else {
            $extra.Add($stripped)
        }
    }
    return @{ Settings = $settings; Extra = $extra }
}

# 值 → 配置文件中的写法：不含单引号时用单引号原样写出（Windows 路径不需要转义），否则用双引号并转义 \ " $ `。
function ConvertTo-EnvValue([string]$Value) {
    if (-not $Value.Contains("'")) { return "'" + $Value + "'" }
    return '"' + ($Value -replace '([\\"$`])', '\$1') + '"'
}

# ---- 路径与权限 ----

function Test-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    return (New-Object Security.Principal.WindowsPrincipal $identity).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)
}

# 绝对路径（盘符或 \\server\share 开头），不含控制字符与双引号；去掉结尾的 \。
function Resolve-InstallPath([string]$Option, [string]$Path) {
    if ($Path -match '[\x00-\x1f"]') { Stop-Install 2 "$Option 不能包含双引号或换行等控制字符：$Path" }
    if ($Path -notmatch '^([A-Za-z]:\\|\\\\[^\\]+\\[^\\]+)') {
        Stop-Install 2 "$Option 必须是绝对路径（如 C:\ProgramData\trainMaster\trainer）：$Path"
    }
    $full = [IO.Path]::GetFullPath($Path)
    if ($full.Length -gt 3) { $full = $full.TrimEnd('\') }
    return $full
}

function Test-SamePath([string]$A, [string]$B) {
    return $A.TrimEnd('\').TrimEnd('/') -ieq $B.TrimEnd('\').TrimEnd('/')
}

# $Path 是否在目录 $Dir 之中（不含 $Dir 本身）。
function Test-UnderDir([string]$Path, [string]$Dir) {
    return $Path.StartsWith($Dir.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)
}

function Get-AccountName([string]$Sid) {
    try {
        return (New-Object Security.Principal.SecurityIdentifier $Sid).Translate([Security.Principal.NTAccount]).Value
    } catch {
        return $Sid
    }
}

# 不必防范的账户：SYSTEM、Administrators、TrustedInstaller、所有者本身，以及运行本脚本的管理员。
function Get-TrustedSids {
    return @($SidSystem, $SidAdmins, $SidTrustedInstaller, $SidOwnerRights,
        [Security.Principal.WindowsIdentity]::GetCurrent().User.Value)
}

# 其他账户能否修改（或替换）这个目录或文件：所有者不是上面的账户，或有适用于它本身的规则授予其他账户
# 删除、改名、修改权限或所有者的权限；-Contents（其中的程序与设置要被信任的目录或文件）时还包括写入、
# 新建文件。返回原因（中文），没有问题时为 $null。
function Get-UnsafeReason([string]$Path, [switch]$Contents) {
    # DeleteSubdirectoriesAndFiles 0x40、Delete 0x10000、ChangePermissions 0x40000、TakeOwnership 0x80000、GENERIC_ALL
    [long]$mask = 0x100D0040
    # WriteData 0x2、AppendData 0x4、WriteExtendedAttributes 0x10、WriteAttributes 0x100、GENERIC_WRITE
    if ($Contents) { $mask = $mask -bor 0x40000116 }
    try {
        $acl = Get-Acl -LiteralPath $Path
        $sidType = [Security.Principal.SecurityIdentifier]
        $trusted = Get-TrustedSids
        $owner = $acl.GetOwner($sidType).Value
        if ($trusted -notcontains $owner) { return "所有者是 $(Get-AccountName $owner)" }
        foreach ($rule in $acl.GetAccessRules($true, $true, $sidType)) {
            if ($rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) { continue }
            if (([int]$rule.PropagationFlags -band 2) -ne 0) { continue }  # InheritOnly：只给子目录与文件
            $sid = $rule.IdentityReference.Value
            if ($trusted -contains $sid) { continue }
            if (([long][int]$rule.FileSystemRights -band $mask) -ne 0) { return "$(Get-AccountName $sid) 可以修改它" }
        }
    } catch {
        return "无法读取权限：$($_.Exception.Message)"
    }
    return $null
}

# 上级目录中其他账户可以替换（改名或删除后重建）的一个：借此可以把整个目录换成自己的程序。
# 返回“目录（原因）”，没有问题时为 $null。磁盘的根目录不检查（普通用户只能在其中新建目录）。
function Get-UnsafeAncestor([string]$Path) {
    $dir = Split-Path -Parent $Path
    while ($dir -and (Split-Path -Parent $dir) -and $dir -notmatch '^\\\\[^\\]+\\[^\\]+$') {
        if (Test-Path -LiteralPath $dir) {
            $reason = Get-UnsafeReason $dir
            if ($reason) { return "$dir（$reason）" }
        }
        $dir = Split-Path -Parent $dir
    }
    return $null
}

# 新的权限：不继承上级目录，只授予 SYSTEM 与管理员完全控制（$ReadUsers 时普通用户可以读取），所有者为 Administrators。
function New-ProtectedAcl([string]$Type, [switch]$ReadUsers) {
    $acl = New-Object "Security.AccessControl.$Type"
    $acl.SetAccessRuleProtection($true, $false)
    $inherit = if ($Type -eq 'DirectorySecurity') { 'ContainerInherit, ObjectInherit' } else { 'None' }
    $grants = @(@($SidSystem, 'FullControl'), @($SidAdmins, 'FullControl'))
    if ($ReadUsers) { $grants += , @($SidUsers, 'ReadAndExecute') }
    foreach ($g in $grants) {
        $sid = New-Object Security.Principal.SecurityIdentifier $g[0]
        $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule $sid, $g[1], $inherit, 'None', 'Allow'))
    }
    $acl.SetOwner((New-Object Security.Principal.SecurityIdentifier $SidAdmins))
    return $acl
}

# 目录只允许 SYSTEM 与管理员修改，普通用户只读：计划任务以 SYSTEM 运行这里的程序，普通用户如果能写入
# （C:\ProgramData 的子目录默认允许普通用户新建文件），就能借此以 SYSTEM 身份运行代码。
# 一次替换整个权限列表（不会有短暂允许普通用户写入的时刻），已有的子目录与文件继承新的权限。
# -Recurse：子目录与文件原有的单独设置的权限也去掉，所有者改为 Administrators（目录原来不安全时）。
function Protect-Directory([string]$Path, [switch]$Recurse) {
    if (-not $Recurse -and (Test-Path -LiteralPath $Path) -and -not (Get-UnsafeReason $Path -Contents) -and
        (Get-Acl -LiteralPath $Path).AreAccessRulesProtected) {
        return  # 已经设置过（重新设置时要更新虚拟环境中所有文件继承的权限，比较慢）
    }
    Write-Host "+ 设置 $Path 的权限：只有 SYSTEM 与管理员可以修改$(if ($Recurse) { '（包括其中已有的文件）' })"
    if ($DryRun) { return }
    try {
        Set-Acl -LiteralPath $Path -AclObject (New-ProtectedAcl 'DirectorySecurity' -ReadUsers)
    } catch {
        Stop-Install 1 ("无法设置 $Path 的权限（$($_.Exception.Message)）：该目录可能是其他用户创建的，" +
            "请运行 takeown /f `"$Path`" /r /a 取得所有权并检查其中的文件后删除它，再重新运行")
    }
    if ($Recurse -and @(Get-ChildItem -LiteralPath $Path -Force).Count -gt 0) {
        Invoke-Step 'icacls.exe' @((Join-Path $Path '*'), '/setowner', "*$SidAdmins", '/T', '/C', '/Q') `
            -Failure "无法设置 $Path 中文件的所有者" | Out-Null
        Invoke-Step 'icacls.exe' @((Join-Path $Path '*'), '/reset', '/T', '/C', '/Q') `
            -Failure "无法设置 $Path 中文件的权限" | Out-Null
    }
}

# 新建只有 SYSTEM 与管理员可以读写的文件（先设置权限再写入内容，令牌不会短暂地对其他用户可读），再替换目标文件。
function Write-ProtectedFile([string]$Path, [string]$Content) {
    $dir = Split-Path -Parent $Path
    $tmp = Join-Path $dir (".{0}.{1}.tmp" -f (Split-Path -Leaf $Path), [guid]::NewGuid().ToString('N').Substring(0, 8))
    [IO.File]::WriteAllText($tmp, '')
    try {
        Set-Acl -LiteralPath $tmp -AclObject (New-ProtectedAcl 'FileSecurity')
        [IO.File]::WriteAllText($tmp, $Content, (New-Object Text.UTF8Encoding $false))
        if (Test-Path -LiteralPath $Path) { Remove-Item -LiteralPath $Path -Force }
        [IO.File]::Move($tmp, $Path)
    } finally {
        if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue }
    }
}

function New-TempDir {
    $dir = Join-Path ([IO.Path]::GetTempPath()) ('trainmaster-install-' + [guid]::NewGuid().ToString('N').Substring(0, 12))
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    $Ctx.TempDirs += $dir
    return $dir
}

function Test-TrainerDir([string]$Dir) {
    return (Test-Path -LiteralPath (Join-Path $Dir 'pyproject.toml')) -and
        (Test-Path -LiteralPath (Join-Path $Dir 'requirements.txt')) -and
        (Test-Path -LiteralPath (Join-Path (Join-Path $Dir 'trainmaster_trainer') '__init__.py'))
}

function Get-TrainerPackage {
    $scriptTrainer = if ($ScriptPath) { Split-Path -Parent (Split-Path -Parent $ScriptPath) } else { $null }
    if ($Package) {
        if (-not (Test-Path -LiteralPath $Package)) { Stop-Install 2 "-Package 不存在：$Package" }
        $item = Get-Item -LiteralPath $Package
        if ($item.PSIsContainer) {
            if (-not (Test-TrainerDir $item.FullName)) {
                Stop-Install 2 "-Package $Package 不是训练节点目录（应包含 pyproject.toml、requirements.txt 与 trainmaster_trainer）"
            }
            return $item.FullName
        }
        $zip = $item.FullName
    } elseif ($scriptTrainer -and (Test-TrainerDir $scriptTrainer)) {
        # 在仓库（或解压的安装包）中运行：使用本脚本所在的 trainer 目录
        return $scriptTrainer
    } else {
        $url = "$ServerUrl/api/v1/compute/windows/trainer.zip"
        $zip = Join-Path (New-TempDir) 'trainmaster-trainer.zip'
        Write-Info "从平台下载训练节点：$url"
        try {
            Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $zip
        } catch {
            $detail = $_.Exception.Message
            Stop-Install 1 ("无法从平台下载训练节点（$detail）。平台没有附带安装包时，在 Windows 电脑上克隆 trainMaster 仓库，" +
                '在仓库目录中运行 trainer\scripts\install.ps1，或用 -Package 指定 trainer 目录')
        }
    }
    $dest = New-TempDir
    try {
        Expand-Archive -LiteralPath $zip -DestinationPath $dest -Force
    } catch {
        Stop-Install 1 "无法解压训练节点安装包 ${zip}：$($_.Exception.Message)"
    }
    if (-not (Test-TrainerDir $dest)) { Stop-Install 1 "安装包 $zip 不完整（缺少 pyproject.toml、requirements.txt 或 trainmaster_trainer）" }
    return $dest
}

# ---- Python ----

# 解释器的版本、位数与路径；不能运行时为 $null。
function Get-PythonInfo([string]$Exe, [string[]]$PrefixArgs = @()) {
    $code = 'import struct, sys; print("%d.%d|%d|%s" % (sys.version_info[0], sys.version_info[1], struct.calcsize("P") * 8, sys.executable))'
    $result = Invoke-Capture $Exe ($PrefixArgs + @('-c', $code))
    if ($null -eq $result -or $result.ExitCode -ne 0 -or -not $result.Stdout.Trim()) { return $null }
    $parts = ($result.Stdout.Trim() -split "`r?`n")[-1].Trim() -split '\|', 3
    if ($parts.Count -ne 3) { return $null }
    try { $version = [version]$parts[0] } catch { return $null }
    return @{ Version = $version; Bits = [int]$parts[1]; Executable = $parts[2] }
}

# 不合适的原因；合适时为 $null。
function Get-PythonProblem($Info) {
    if ($null -eq $Info) { return '无法运行' }
    if ($Info.Bits -ne 64) { return '是 32 位的 Python（需要 64 位）' }
    if ($Info.Version -lt $PythonVersionMin -or $Info.Version -gt $PythonVersionMax) {
        return "版本为 $($Info.Version)（需要 3.10–3.13）"
    }
    if ($Info.Executable -match '\\WindowsApps\\') {
        return '是 Microsoft Store 版的 Python（以 SYSTEM 运行的计划任务无法使用），请从 python.org 或用 winget 安装'
    }
    return $null
}

# 优先使用为所有用户安装的 Python（C:\Program Files），其次是 py 启动器、当前用户安装的与 PATH 中的。
function Find-Python {
    $candidates = New-Object System.Collections.Generic.List[object]
    foreach ($dir in 'Python312', 'Python313', 'Python311', 'Python310') {
        $candidates.Add(@((Join-Path $env:ProgramFiles "$dir\python.exe"), @()))
    }
    if (Get-Command py.exe -ErrorAction SilentlyContinue) {
        foreach ($v in '3.12', '3.13', '3.11', '3.10') { $candidates.Add(@('py.exe', @("-$v"))) }
    }
    if ($env:LOCALAPPDATA) {
        foreach ($dir in 'Python312', 'Python313', 'Python311', 'Python310') {
            $candidates.Add(@((Join-Path $env:LOCALAPPDATA "Programs\Python\$dir\python.exe"), @()))
        }
    }
    $onPath = Get-Command python.exe -ErrorAction SilentlyContinue
    if ($onPath) { $candidates.Add(@($onPath.Source, @())) }
    foreach ($c in $candidates) {
        $exe = $c[0]
        if ($exe -ne 'py.exe' -and -not (Test-Path -LiteralPath $exe)) { continue }
        $info = Get-PythonInfo $exe $c[1]
        if ($null -eq (Get-PythonProblem $info)) { return $info.Executable }
    }
    return $null
}

function Get-WingetPath {
    $cmd = Get-Command winget.exe -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    return $null
}

function Install-PythonRuntime {
    $winget = Get-WingetPath
    if ($winget) {
        $code = Invoke-Step $winget @('install', '--id', $PythonWingetId, '--exact', '--scope', 'machine', '--silent',
            '--accept-package-agreements', '--accept-source-agreements', '--disable-interactivity') -AllowFailure
        if ($DryRun) { return (Join-Path $env:ProgramFiles "$PythonDefaultDir\python.exe") }
        $found = Find-Python
        if ($found) { return $found }
        Write-Warn "winget 安装 Python 没有成功（退出码 $code），改为下载 python.org 的安装程序"
    }
    $url = if ($env:TM_PYTHON_INSTALLER_URL) { $env:TM_PYTHON_INSTALLER_URL } else { $PythonInstallerUrl }
    if ($DryRun) {
        Write-Host "+ 下载 $url 并静默安装（所有用户）"
        return (Join-Path $env:ProgramFiles "$PythonDefaultDir\python.exe")
    }
    $exe = Join-Path (New-TempDir) 'python-installer.exe'
    Write-Info "下载 Python 安装程序：$url"
    try { Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $exe } catch {
        Stop-Install 1 "无法下载 Python 安装程序（$($_.Exception.Message)）：请手动安装 64 位 Python 3.12 后重新运行"
    }
    Invoke-Step $exe @('/quiet', 'InstallAllUsers=1', 'PrependPath=0', 'Include_test=0', 'Include_launcher=1') `
        -Failure 'Python 安装程序运行失败：请手动安装 64 位 Python 3.12 后重新运行' | Out-Null
    $found = Find-Python
    if (-not $found) { Stop-Install 1 '安装 Python 后仍然找不到它：请用 -Python 指定 python.exe 的路径' }
    return $found
}

# Microsoft Visual C++ 2015–2022 运行库（x64）：PyTorch 的 DLL 依赖它。
function Test-VcRuntime {
    foreach ($key in 'HKLM:\SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\X64',
        'HKLM:\SOFTWARE\WOW6432Node\Microsoft\VisualStudio\14.0\VC\Runtimes\X64') {
        $item = Get-ItemProperty -Path $key -ErrorAction SilentlyContinue
        if ($item -and $item.Installed -eq 1) { return $true }
    }
    $sys = Join-Path $env:SystemRoot 'System32'
    return (Test-Path (Join-Path $sys 'vcruntime140_1.dll')) -and (Test-Path (Join-Path $sys 'msvcp140.dll'))
}

function Install-VcRuntime {
    $winget = Get-WingetPath
    if ($winget) {
        Invoke-Step $winget @('install', '--id', 'Microsoft.VCRedist.2015+.x64', '--exact', '--silent',
            '--accept-package-agreements', '--accept-source-agreements', '--disable-interactivity') -AllowFailure | Out-Null
        if ($DryRun -or (Test-VcRuntime)) { return }
        Write-Warn 'winget 安装 VC++ 运行库没有成功，改为下载微软的安装程序'
    }
    $url = if ($env:TM_VC_REDIST_URL) { $env:TM_VC_REDIST_URL } else { $VcRedistUrl }
    if ($DryRun) { Write-Host "+ 下载 $url 并静默安装"; return }
    $exe = Join-Path (New-TempDir) 'vc_redist.x64.exe'
    try { Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $exe } catch {
        Stop-Install 1 "无法下载 VC++ 运行库（$($_.Exception.Message)）：请手动安装 $VcRedistUrl 后重新运行"
    }
    $code = Invoke-Step $exe @('/install', '/quiet', '/norestart') -AllowFailure
    # 3010：需要重启才能完成；1638：已安装了更新的版本
    if ($code -ne 0 -and $code -ne 3010 -and $code -ne 1638) {
        Stop-Install 1 "VC++ 运行库安装失败（退出码 $code）：请手动安装 $VcRedistUrl 后重新运行"
    }
}

# 用 python 运行 trainmaster_trainer recommend（检测显卡并选择变体，见 variants.py），返回解析后的 JSON。
function Get-Recommendation([string]$PythonExe, [string]$PackageDir) {
    $recArgs = @('-s', '-m', 'trainmaster_trainer', 'recommend', '--json')
    if ($Variant) { $recArgs += @('--variant', $Variant) }
    $result = Invoke-Capture $PythonExe $recArgs @{ PYTHONPATH = $PackageDir }
    if ($null -eq $result) { Stop-Install 1 "无法运行 $PythonExe" }
    if ($result.ExitCode -eq 2) { Stop-Install 2 (Get-Tail $result.Stderr 2) }
    if ($result.ExitCode -ne 0) { Stop-Install 1 ("检测显卡失败：`n" + (Get-Tail $result.Stderr)) }
    try {
        return ($result.Stdout | ConvertFrom-Json)
    } catch {
        Stop-Install 1 "检测显卡失败：无法解析输出 $($result.Stdout)"
    }
}

# ---- 计划任务 ----

function Get-OurTask {
    return Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue | Select-Object -First 1
}

# 计划任务的程序是否为本安装目录的 python.exe；是时返回其 --env-file 参数（没有时为空字符串），否则 $null。
function Get-TaskEnvFile($Task, [string]$VenvPython) {
    if ($null -eq $Task) { return $null }
    foreach ($action in $Task.Actions) {
        if ($null -eq $action.Execute) { continue }
        if (-not (Test-SamePath $action.Execute.Trim('"') $VenvPython)) { continue }
        if ($action.Arguments -match '--env-file\s+"([^"]+)"') { return $Matches[1] }
        if ($action.Arguments -match '--env-file\s+(\S+)') { return $Matches[1] }
        return ''
    }
    return $null
}

# 停止计划任务并等待训练节点退出（替换虚拟环境中的文件之前；运行中的 DLL 无法覆盖）。
function Stop-Trainer([string]$EnvFilePath) {
    $task = Get-OurTask
    if ($task -and $task.State -eq 'Running') {
        Write-Host "+ Stop-ScheduledTask -TaskName $TaskName"
        if (-not $DryRun) {
            Stop-ScheduledTask -TaskName $TaskName
            for ($i = 0; $i -lt 30 -and (Get-OurTask).State -eq 'Running'; $i++) { Start-Sleep -Seconds 1 }
        }
    }
    if ($DryRun) { return }
    # 手动运行（或停止计划任务后仍在退出）的训练节点：命令行中有 trainmaster_trainer 与本配置文件
    $procs = @(Get-CimInstance Win32_Process -Filter "Name = 'python.exe'" -ErrorAction SilentlyContinue | Where-Object {
            $cmd = [string]$_.CommandLine
            $cmd.IndexOf('trainmaster_trainer', [StringComparison]::OrdinalIgnoreCase) -ge 0 -and
            $cmd.IndexOf($EnvFilePath, [StringComparison]::OrdinalIgnoreCase) -ge 0
        })
    foreach ($p in $procs) {
        Write-Info "结束仍在运行的训练节点进程 $($p.ProcessId)"
        Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue
    }
    foreach ($p in $procs) { Wait-Process -Id $p.ProcessId -Timeout 30 -ErrorAction SilentlyContinue }
}

function Register-TrainerTask([string]$VenvPython, [string]$EnvFilePath, [string]$WorkDir) {
    $arguments = "-m trainmaster_trainer run --env-file `"$EnvFilePath`""
    Write-Host "+ Register-ScheduledTask -TaskName $TaskName（开机时以 SYSTEM 运行：`"$VenvPython`" $arguments）"
    Write-Host "+ Start-ScheduledTask -TaskName $TaskName"
    if ($DryRun) { return }
    $action = New-ScheduledTaskAction -Execute "`"$VenvPython`"" -Argument $arguments -WorkingDirectory $WorkDir
    # 开机时启动；另有一个从现在开始的触发器。两者每 5 分钟重复一次：训练节点没有在运行时（崩溃、被结束）
    # 重新启动它，已在运行时忽略（MultipleInstances IgnoreNew）。
    $repeat = (New-ScheduledTaskTrigger -Once -At (Get-Date) -RepetitionInterval (New-TimeSpan -Minutes 5)).Repetition
    $boot = New-ScheduledTaskTrigger -AtStartup
    $boot.Repetition = $repeat
    $now = New-ScheduledTaskTrigger -Once -At ((Get-Date).AddMinutes(1)) -RepetitionInterval (New-TimeSpan -Minutes 5)
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable `
        -MultipleInstances IgnoreNew -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 999 `
        -RestartInterval (New-TimeSpan -Minutes 1)
    $settings.Priority = 4  # 计划任务默认以低于正常的优先级运行（7），训练节点使用正常优先级
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger @($boot, $now) -Principal $principal `
        -Settings $settings -Description 'trainMaster 训练节点（由 trainer\scripts\install.ps1 注册）' -Force | Out-Null
    Start-ScheduledTask -TaskName $TaskName
}

# 接通电源时多久无操作后睡眠（秒，0 为从不）；读不到时为 $null。
function Get-AcSleepSeconds {
    try {
        $plan = Get-CimInstance -Namespace root\cimv2\power -ClassName Win32_PowerPlan -Filter 'IsActive = True' -ErrorAction Stop |
            Select-Object -First 1
        if (-not $plan) { return $null }
        $planId = ($plan.InstanceID -split '\\')[-1]
        $id = "Microsoft:PowerSettingDataIndex\$planId\AC\{29f6c1db-86da-48c5-9fdb-f2b67b1f44da}"
        $index = Get-CimInstance -Namespace root\cimv2\power -ClassName Win32_PowerSettingDataIndex -ErrorAction Stop |
            Where-Object { $_.InstanceID -eq $id } | Select-Object -First 1
        if ($index) { return [int]$index.SettingIndexValue }
    } catch {
        return $null
    }
    return $null
}

# ==== 安装 ====
# 在函数中运行：其中的 return 结束安装，之后仍会执行最后报告退出码的代码。函数中给参数（$Prefix 等）
# 赋值时创建的是函数的局部变量，它调用的函数通过作用域链读到的是这些局部变量。

function Invoke-Install {
    if ($Help) {
        Write-Host @'
用法：install.ps1 -ServerUrl URL [-Token 令牌] [-Name 名称] [-Variant 变体] [-Service] [-InstallPython] [-DryRun]
              [-Prefix 目录] [-Python python.exe] [-StateDir 目录] [-DataDir 目录] [-EnvFile 文件] [-Package 目录或zip]
       install.ps1 -Uninstall

在本机安装 trainMaster 训练节点：自动检测显卡与驱动，安装匹配的 CUDA 版 PyTorch 与 Ultralytics，写入配置文件，
并可注册为开机自启的计划任务（-Service，以 SYSTEM 运行）。需要以管理员身份运行。
完整说明：Get-Help <install.ps1 的路径> -Detailed
'@
        return
    }

    if ($env:OS -ne 'Windows_NT') { Stop-Install 1 '本脚本只能在 Windows 上运行（Linux 请使用 trainer/scripts/install.sh）' }
    if (-not [Environment]::Is64BitOperatingSystem) { Stop-Install 1 '需要 64 位 Windows' }
    $arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    if ($arch -ne 'AMD64') { Stop-Install 1 "只支持 x64（AMD64）的 Windows，本机为 $arch" }
    $build = [Environment]::OSVersion.Version.Build
    if ($build -lt 17763) { Write-Warn "本机的 Windows 版本较旧（内部版本 $build）：推荐 Windows 10 1809 / Windows Server 2019 或更新" }
    if (-not $DryRun -and -not (Test-Administrator)) {
        Stop-Install 1 '请以管理员身份运行 PowerShell（右键“开始”→“终端（管理员）”或“Windows PowerShell（管理员）”）后重新运行'
    }

    # ---- 路径 ----
    $programData = if ($env:ProgramData) { $env:ProgramData } else { 'C:\ProgramData' }
    $tmRoot = Join-Path $programData 'trainMaster'
    $Prefix = if ($Prefix) { Resolve-InstallPath '-Prefix' $Prefix } else { Join-Path $tmRoot 'trainer' }
    if ($Prefix -match '^[A-Za-z]:\\?$') { Stop-Install 2 '-Prefix 不能是磁盘的根目录' }
    $venvPython = Join-Path $Prefix 'Scripts\python.exe'
    $task = Get-OurTask
    $taskEnvFile = Get-TaskEnvFile $task $venvPython
    $taskInstalled = $null -ne $taskEnvFile

    if ($Uninstall) {
        if ($null -eq $task) {
            Write-Info "没有注册计划任务 $TaskName"
        } else {
            Stop-Trainer $(if ($taskEnvFile) { $taskEnvFile } else { Join-Path $Prefix 'trainer.env' })
            Write-Host "+ Unregister-ScheduledTask -TaskName $TaskName -Confirm:`$false"
            if (-not $DryRun) { Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false }
            Write-Info "已删除计划任务 $TaskName，训练节点不再运行"
        }
        Write-Host "安装目录 $Prefix 与状态目录没有删除；不再需要时可以手动删除（Remove-Item -Recurse -Force <目录>）。"
        Write-Host '在平台的“计算资源”页面删除这个节点（节点离线后才能删除）。'
        return
    }

    # 没有指定 -EnvFile 时，已注册的计划任务使用的配置文件优先（首次安装时可能用 -EnvFile 指定了其它路径）
    if ($EnvFile) {
        $EnvFile = Resolve-InstallPath '-EnvFile' $EnvFile
    } elseif ($taskEnvFile) {
        $EnvFile = $taskEnvFile
    } else {
        $EnvFile = Join-Path $Prefix 'trainer.env'
    }

    # ---- 沿用已有的配置 ----
    $existing = @{ Settings = @{}; Extra = @() }
    $envFileUnsafe = $null
    if (Test-Path -LiteralPath $EnvFile) {
        $readable = $true
        try { [IO.File]::OpenRead($EnvFile).Dispose() } catch { $readable = $false }
        if ($readable) {
            $existing = Read-EnvFile $EnvFile
            $envFileUnsafe = Get-UnsafeReason $EnvFile -Contents
        } elseif (-not $DryRun) {
            Stop-Install 1 "无法读取已有的配置文件 $EnvFile（请以管理员身份运行）"
        } else {
            Write-Info "无法读取已有的配置文件 $EnvFile，预览中不沿用其中的设置（实际安装请以管理员身份运行）"
        }
    }
    $from = $existing.Settings
    $reused = @()
    if (-not $ServerUrl -and $from['TM_SERVER_URL']) { $ServerUrl = $from['TM_SERVER_URL']; $reused += 'TM_SERVER_URL' }
    if (-not $TokenGiven -and $from['TM_NODE_TOKEN']) { $Token = $from['TM_NODE_TOKEN']; $reused += 'TM_NODE_TOKEN' }
    if (-not $Name -and $from['TM_NODE_NAME']) { $Name = $from['TM_NODE_NAME']; $reused += 'TM_NODE_NAME' }
    if (-not $StateDir -and $from['TM_STATE_DIR']) { $StateDir = $from['TM_STATE_DIR']; $reused += 'TM_STATE_DIR' }
    if (-not $DataDir -and $from['TM_DATA_DIR']) { $DataDir = $from['TM_DATA_DIR']; $reused += 'TM_DATA_DIR' }
    $logFile = $null
    if (-not $StateDirGiven -and $from['TM_LOG_FILE']) { $logFile = $from['TM_LOG_FILE']; $reused += 'TM_LOG_FILE' }
    $cudaOrder = if ($from['CUDA_DEVICE_ORDER']) { $from['CUDA_DEVICE_ORDER'] } else { 'PCI_BUS_ID' }
    $extraKeys = @($existing.Extra | ForEach-Object { ($_ -split '=', 2)[0].Trim() } | Select-Object -Unique)

    # ---- 校验 ----
    if (-not $ServerUrl) {
        Stop-Install 2 '缺少 -ServerUrl（平台地址，例如 http://192.168.1.10:8080）'
    }
    if ($ServerUrl -match '[\x00-\x1f]' -or $ServerUrl -notmatch '^https?://[^\s/?#@]+(/[^\s?#]*)?$') {
        Stop-Install 2 "-ServerUrl 不合法：$ServerUrl（应为 http://主机:端口，不能包含用户名、查询参数或空格）"
    }
    $ServerUrl = $ServerUrl.TrimEnd('/')
    if ($Token -and ($Token -notmatch '^[!-~]+$' -or $Token.Length -gt 256)) {
        Stop-Install 2 '-Token 只能包含可见的 ASCII 字符（不能有空格或中文），最多 256 个字符'
    }
    if (-not $Name) { $Name = [Net.Dns]::GetHostName() }
    if (-not $Name) { $Name = $env:COMPUTERNAME }
    if ($Name -match '[\x00-\x1f\x7f]') { Stop-Install 2 '-Name 不能包含换行等控制字符' }
    $StateDir = if ($StateDir) { Resolve-InstallPath '-StateDir' $StateDir } else { Join-Path $tmRoot 'state' }
    if ($DataDir) { $DataDir = Resolve-InstallPath '-DataDir' $DataDir }
    $logFile = if ($logFile) { Resolve-InstallPath 'TM_LOG_FILE' $logFile } else { Join-Path $StateDir 'logs\trainer.log' }
    if ($cudaOrder -match '[\x00-\x1f]') { Stop-Install 2 '配置文件中的 CUDA_DEVICE_ORDER 不能包含换行等控制字符' }
    $serviceFound = $taskInstalled -and -not $Service
    $registerTask = $Service -or $taskInstalled

    if ($reused.Count -gt 0) { Write-Info "沿用配置文件 $EnvFile 中的 $($reused -join ' ')" }
    if ($extraKeys.Count -gt 0) { Write-Info "保留配置文件中的其它设置：$($extraKeys -join ' ')" }
    if ($envFileUnsafe) {
        Write-Warn ("配置文件 $EnvFile 允许其他用户修改（$envFileUnsafe），安装时改为只有 SYSTEM 与管理员可以读写。" +
            '训练节点以 SYSTEM 运行并使用其中的所有设置：请确认上面保留的设置都是你添加的')
    }
    if ($serviceFound) { Write-Info "已注册计划任务 ${TaskName}：安装后更新并重启它" }
    if ($task -and -not $taskInstalled -and $Service) {
        Write-Warn "已有同名的计划任务 $TaskName（运行的不是 $venvPython），将替换它"
    }

    # ---- 目录的权限 ----
    # 默认的目录都在 C:\ProgramData\trainMaster 中，安装时设置它的权限；其它位置检查上级目录能否被其他用户替换。
    $useRoot = (Test-UnderDir $Prefix $tmRoot) -or (Test-UnderDir $StateDir $tmRoot) -or (Test-UnderDir $EnvFile $tmRoot)
    $warned = @{}
    foreach ($p in @($Prefix, $StateDir, $EnvFile)) {
        if ((Test-UnderDir $p $tmRoot) -or ($p -ne $Prefix -and (Test-UnderDir $p $Prefix))) { continue }
        $bad = Get-UnsafeAncestor $p
        if ($bad -and -not $warned.ContainsKey($bad)) {
            $warned[$bad] = $true
            Write-Warn ("$p 的上级目录 $bad 允许其他用户改名或删除：其他用户可以借此把训练节点换成自己的程序，" +
                '以 SYSTEM 身份运行。建议使用默认目录，或只有管理员可以修改的目录')
        }
    }
    $prefixUnsafe = if (Test-Path -LiteralPath $Prefix) { Get-UnsafeReason $Prefix -Contents } else { $null }
    $stateUnsafe = if (Test-Path -LiteralPath $StateDir) { Get-UnsafeReason $StateDir -Contents } else { $null }

    # ---- 安装包与 Python ----
    $packageDir = Get-TrainerPackage
    $basePython = $null
    $pythonToInstall = $false
    $venvExists = Test-Path -LiteralPath $venvPython
    if ($venvExists) {
        if ($prefixUnsafe) {
            Stop-Install 1 ("已有的安装目录 $Prefix 允许其他用户修改（$prefixUnsafe），其中的程序可能被改动过：" +
                '请删除该目录后重新运行')
        }
        $problem = Get-PythonProblem (Get-PythonInfo $venvPython)
        if ($problem) {
            Stop-Install 1 "已有的虚拟环境 $Prefix 的 Python $problem（可能系统的 Python 已卸载或升级），请删除该目录后重新运行"
        }
        $detectPython = $venvPython
    } else {
        if ((Test-Path -LiteralPath $Prefix) -and @(Get-ChildItem -LiteralPath $Prefix -Force).Count -gt 0) {
            Stop-Install 1 "$Prefix 已存在且不是虚拟环境，请换一个 -Prefix 或先清空该目录"
        }
        if ($Python) {
            $info = Get-PythonInfo $Python
            $problem = Get-PythonProblem $info
            if ($problem) { Stop-Install 1 "-Python $Python $problem" }
            $basePython = $info.Executable
        } else {
            $basePython = Find-Python
        }
        if (-not $basePython) {
            if (-not $InstallPython) {
                Stop-Install 1 ('没有找到 64 位的 Python 3.10–3.13：加上 -InstallPython 自动安装 Python 3.12，' +
                    '或从 https://www.python.org/downloads/windows/ 安装（勾选 Install for all users）后重新运行，或用 -Python 指定')
            }
            $pythonToInstall = $true
        } elseif (Test-UnderDir $basePython ([Environment]::GetFolderPath('UserProfile'))) {
            Write-Warn ("使用当前用户安装的 Python（$basePython）：训练节点依赖它，卸载或升级它之后需要删除 " +
                "$Prefix 并重新运行本脚本。为所有用户安装的 Python 更合适（加上 -InstallPython 自动安装）")
        }
        $detectPython = $basePython
    }
    $vcMissing = -not (Test-VcRuntime)
    if ($vcMissing -and -not $InstallPython -and -not $DryRun) {
        Stop-Install 1 "缺少 Microsoft Visual C++ 运行库（PyTorch 需要）：安装 $VcRedistUrl 后重新运行，或加上 -InstallPython 自动安装"
    }

    # ---- 检测显卡、选择变体（与 Linux 的 install.sh 使用同样的规则，见 trainmaster_trainer/variants.py）----
    $report = $null
    if ($detectPython) { $report = Get-Recommendation $detectPython $packageDir }

    Write-Host '== 检测结果 =='
    Write-Host "系统：$((Get-CimInstance Win32_OperatingSystem -ErrorAction SilentlyContinue).Caption)（内部版本 $build，$arch）"
    if ($null -eq $report) {
        Write-Host '显卡：安装 Python 之后检测'
        $chosen = $null
    } else {
        $gpus = @($report.gpus)
        if ($gpus.Count -gt 0) {
            Write-Host "显卡：$($gpus.Count) 块"
            foreach ($g in $gpus) {
                $cc = if ($g.compute_capability) { $g.compute_capability } else { "$($report.assumed_cc)（估计）" }
                $mem = if ($g.memory_total_bytes) { '显存 {0:N0} GiB，' -f ($g.memory_total_bytes / 1GB) } else { '' }
                Write-Host "  [$($g.index)] $($g.name)（${mem}计算能力 $cc）"
            }
            Write-Host "驱动：$(if ($report.driver) { $report.driver } else { '未知' })"
        } elseif ($report.pci_gpu_count) {
            Write-Host "显卡：检测到 $($report.pci_gpu_count) 块 NVIDIA 显卡，但没有可用的驱动。安装 NVIDIA 显卡驱动（推荐 580 或更新）并重启后重新运行本脚本，才能使用 GPU 训练"
        } else {
            Write-Host '显卡：未检测到 NVIDIA 显卡（将作为 CPU 训练节点）'
        }
        Write-Host "推荐的 PyTorch 变体：$($report.recommended.variant) —— $($report.recommended.reason)"
        $chosen = $report.variant
        if ($chosen.name -ne $report.recommended.variant) { Write-Host "使用手动指定的变体：$($chosen.name)" }
        if ($chosen.name -eq 'cpu' -and $gpus.Count -gt 0) { Write-Host '注意：将安装 CPU 版 PyTorch，训练不会使用显卡' }
    }
    if ($vcMissing) { Write-Host '注意：缺少 Microsoft Visual C++ 运行库（PyTorch 需要），将自动安装' }
    Write-Host ''

    # ---- 安装 ----
    Write-Host '== 安装步骤 =='
    if ($pythonToInstall) {
        Write-Info '没有合适的 Python，安装 Python 3.12'
        $basePython = Install-PythonRuntime
    }
    if ($vcMissing) { Install-VcRuntime }
    if ($null -eq $chosen -and -not $DryRun) {
        # Python 刚安装：现在检测显卡
        $report = Get-Recommendation $basePython $packageDir
        $chosen = $report.variant
        Write-Info "PyTorch 变体：$($chosen.name) —— $($report.recommended.reason)"
    }

    Stop-Trainer $EnvFile
    if ($useRoot) {
        if (-not (Test-Path -LiteralPath $tmRoot)) {
            Write-Host "+ 创建 $tmRoot"
            if (-not $DryRun) { New-Item -ItemType Directory -Path $tmRoot -Force | Out-Null }
        }
        Protect-Directory $tmRoot
    }
    if ($venvExists) {
        Write-Info "复用已有的虚拟环境 $Prefix"
    } elseif (-not (Test-Path -LiteralPath $Prefix)) {
        Write-Host "+ 创建 $Prefix"
        if (-not $DryRun) { New-Item -ItemType Directory -Path $Prefix -Force | Out-Null }
    }
    Protect-Directory $Prefix
    if (-not $venvExists) {
        # 设置权限之前其他用户可能放入了文件
        if (-not $DryRun -and @(Get-ChildItem -LiteralPath $Prefix -Force).Count -gt 0) {
            Stop-Install 1 "$Prefix 中出现了其它文件（可能是其他用户放入的），请检查并清空该目录后重新运行"
        }
        $py = if ($basePython) { $basePython } else { 'python.exe' }
        Invoke-Step $py @('-m', 'venv', $Prefix) -Failure "创建虚拟环境 $Prefix 失败" | Out-Null
    }
    Invoke-Step $venvPython @('-m', 'pip', 'install', '--disable-pip-version-check', '--upgrade', 'pip') | Out-Null

    # PyTorch：Windows 版一律从 PyTorch 官方索引（或其镜像）安装，版本号带 +<变体>（PyPI 上的 Windows 版只有 CPU 版）
    if ($chosen) {
        $variantName = $chosen.name
        $torchArgs = @('-m', 'pip', 'install', '--disable-pip-version-check')
        $torchIndex = if ($env:TORCH_INDEX_URL) { $env:TORCH_INDEX_URL.TrimEnd('/') } else { $DefaultTorchIndex }
        if ($env:TORCH_FIND_LINKS) {
            if ($env:PIP_INDEX_URL) { $torchArgs += @('--index-url', $env:PIP_INDEX_URL) }
            $torchArgs += @('--find-links', "$($env:TORCH_FIND_LINKS.TrimEnd('/'))/$variantName/")
        } elseif ($env:PIP_INDEX_URL) {
            $torchArgs += @('--index-url', "$torchIndex/$variantName", '--extra-index-url', $env:PIP_INDEX_URL)
        } else {
            $torchArgs += @('--extra-index-url', "$torchIndex/$variantName")
        }
        $torchArgs += @("torch==$($chosen.torch)+$variantName", "torchvision==$($chosen.torchvision)+$variantName")
        Write-Info "安装 PyTorch $($chosen.torch) / torchvision $($chosen.torchvision)（变体 $variantName，CUDA $(if ($chosen.cuda) { $chosen.cuda } else { '无' })）"
        Invoke-Step $venvPython $torchArgs -Failure 'PyTorch 安装失败：检查网络（可设置 $env:PIP_INDEX_URL 与 $env:TORCH_FIND_LINKS 使用镜像）后重新运行' | Out-Null
        # 确认装上的确实是该变体（torch.version.cuda 与变体表一致）
        $expected = if ($chosen.cuda) { $chosen.cuda } else { 'None' }
        $check = 'import torch, torchvision; print(torch.__version__); print(torch.version.cuda); print(torchvision.__version__)'
        Write-Command $venvPython @('-c', $check)
        if (-not $DryRun) {
            $result = Invoke-Capture $venvPython @('-c', $check)
            $got = @()
            if ($result -and $result.ExitCode -eq 0) { $got = @($result.Stdout.Trim() -split "`r?`n" | ForEach-Object { $_.Trim() }) }
            if ($got.Count -lt 3) {
                $detail = if ($result) { Get-Tail $result.Stderr } else { '' }
                Stop-Install 1 ("无法导入安装的 PyTorch：`n$detail`n" +
                    "缺少 VC++ 运行库（WinError 126）时安装 $VcRedistUrl 后重新运行；虚拟内存不足（WinError 1455）时调大页面文件")
            }
            if ($got[1] -ne $expected) {
                Stop-Install 1 "安装的 PyTorch（$($got[0])，CUDA $($got[1])）不是变体 $variantName（CUDA $expected）：镜像中可能没有该变体，请检查 TORCH_INDEX_URL / TORCH_FIND_LINKS"
            }
            Write-Info "已安装 torch $($got[0])（CUDA $($got[1])）、torchvision $($got[2])"
        }
    } else {
        $variantName = '<安装 Python 后自动选择>'
        Write-Host '+ 安装与显卡匹配的 PyTorch（安装 Python 后选择变体）'
    }
    Invoke-Step $venvPython @('-m', 'pip', 'install', '--disable-pip-version-check', '-r', (Join-Path $packageDir 'requirements.txt')) | Out-Null
    Invoke-Step $venvPython @('-m', 'pip', 'install', '--disable-pip-version-check', '--no-deps', '--force-reinstall', $packageDir) | Out-Null

    if (-not (Test-Path -LiteralPath $StateDir)) {
        Write-Host "+ 创建 $StateDir"
        if (-not $DryRun) { New-Item -ItemType Directory -Path $StateDir -Force | Out-Null }
    }
    # 原来允许其他用户修改时，其中已有的文件（如节点标识）也重新设置
    Protect-Directory $StateDir -Recurse:([bool]$stateUnsafe)
    if (-not $DryRun) { New-Item -ItemType Directory -Path (Split-Path -Parent $logFile) -Force | Out-Null }
    if ($DataDir -and -not (Test-Path -LiteralPath $DataDir)) {
        Write-Info "数据目录 $DataDir 不存在：训练节点将上报状态目录所在磁盘的空间（创建该目录后重启训练节点即可）"
    }

    # ---- 配置文件 ----
    Write-Host ''
    Write-Host "== 配置文件 $EnvFile（只有 SYSTEM 与管理员可读）=="
    $tokenText = if ($DryRun) { if ($Token) { "'<已隐藏，共 $($Token.Length) 个字符>'" } else { "''" } } else { ConvertTo-EnvValue $Token }
    $content = New-Object System.Collections.Generic.List[string]
    $content.Add('# trainMaster 训练节点配置（由 trainer\scripts\install.ps1 生成，可手动修改，修改后重启训练节点：')
    $content.Add("#   Stop-ScheduledTask -TaskName $TaskName; Start-ScheduledTask -TaskName $TaskName）。")
    $content.Add('# 重新运行 install.ps1 时，没有通过参数指定的值沿用这里的设置，手动添加的其它设置也会保留（注释除外）。')
    $content.Add('TM_SERVER_URL=' + (ConvertTo-EnvValue $ServerUrl))
    $content.Add('TM_NODE_TOKEN=' + $tokenText)
    $content.Add('TM_NODE_NAME=' + (ConvertTo-EnvValue $Name))
    $content.Add('TM_TORCH_VARIANT=' + (ConvertTo-EnvValue $variantName))
    $content.Add('TM_STATE_DIR=' + (ConvertTo-EnvValue $StateDir))
    if ($DataDir) { $content.Add('TM_DATA_DIR=' + (ConvertTo-EnvValue $DataDir)) }
    $content.Add('# 日志（按大小轮转）')
    $content.Add('TM_LOG_FILE=' + (ConvertTo-EnvValue $logFile))
    $content.Add('# CUDA 按 PCI 总线顺序给显卡编号，与 nvidia-smi 的序号一致')
    $content.Add('CUDA_DEVICE_ORDER=' + (ConvertTo-EnvValue $cudaOrder))
    foreach ($line in $existing.Extra) { $content.Add($line) }
    $text = ($content -join "`r`n") + "`r`n"
    if ($DryRun) {
        Write-Host $text
    } else {
        New-Item -ItemType Directory -Path (Split-Path -Parent $EnvFile) -Force | Out-Null
        Write-ProtectedFile $EnvFile $text
        Write-Info "已写入 $EnvFile"
    }

    # ---- 自检（临时状态目录，不在正式的状态目录中留下文件）----
    Write-Host ''
    Write-Host '== 自检 =='
    $selftestOk = $true
    if ($DryRun) {
        Write-Host "+ `"$venvPython`" -m trainmaster_trainer selftest"
    } else {
        $checkDir = New-TempDir
        $saved = @{}
        $overrides = @{ TM_STATE_DIR = $checkDir; TM_DATA_DIR = $checkDir; TM_TORCH_VARIANT = $variantName; TM_ENV_FILE = $EnvFile }
        foreach ($k in $overrides.Keys) { $saved[$k] = [Environment]::GetEnvironmentVariable($k); [Environment]::SetEnvironmentVariable($k, $overrides[$k]) }
        try {
            $code = Invoke-Step $venvPython @('-m', 'trainmaster_trainer', 'selftest') -AllowFailure
            $selftestOk = $code -eq 0
        } finally {
            foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k]) }
        }
    }

    # ---- 计划任务 ----
    if ($registerTask) {
        Write-Host ''
        Write-Host "== 计划任务 $TaskName =="
        Register-TrainerTask $venvPython $EnvFile $StateDir
    }

    Write-Host ''
    if ($DryRun) {
        Write-Host '以上为预览（-DryRun），没有做任何修改。'
        return
    }
    if ($selftestOk) {
        Write-Host "安装完成：PyTorch 变体 $variantName，自检通过。" -ForegroundColor Green
    } else {
        Write-Host "安装完成，但自检发现问题（见上面的输出）。修复后可运行：& `"$venvPython`" -m trainmaster_trainer selftest --env-file `"$EnvFile`"" -ForegroundColor Yellow
    }
    if ($registerTask) {
        Write-Host '训练节点已作为计划任务运行（开机自动启动，不需要登录）：'
        Write-Host "  查看状态：Get-ScheduledTask -TaskName $TaskName"
        Write-Host "  查看日志：Get-Content -Wait -Tail 50 `"$logFile`""
        Write-Host "  修改配置：编辑 $EnvFile 后运行 Stop-ScheduledTask -TaskName $TaskName; Start-ScheduledTask -TaskName $TaskName"
        Write-Host "  永久停止：Stop-ScheduledTask -TaskName $TaskName; Unregister-ScheduledTask -TaskName $TaskName -Confirm:`$false"
    } else {
        Write-Host '手动运行训练节点（在管理员 PowerShell 中，关闭窗口后停止）：'
        Write-Host "  & `"$venvPython`" -m trainmaster_trainer run --env-file `"$EnvFile`""
        Write-Host '注册为开机自启的计划任务：重新运行本脚本并加上 -Service'
    }
    $sleep = Get-AcSleepSeconds
    if ($sleep -and $sleep -gt 0) {
        Write-Warn ("这台电脑接通电源时 $([math]::Round($sleep / 60)) 分钟无操作后会睡眠，睡眠期间训练节点离线：" +
            '在“设置 → 系统 → 电源”中把睡眠改为“从不”，或运行 powercfg /change standby-timeout-ac 0')
    }
    Write-Host '节点上线后会出现在平台的“计算资源”页面。'
}

# ==== 主流程 ====

try {
    # 子进程（pip、自检）的输出经过 PowerShell 显示：Python 以 UTF-8 输出，PowerShell 也按 UTF-8 解码
    try { $Ctx.ConsoleEncoding = [Console]::OutputEncoding; [Console]::OutputEncoding = New-Object Text.UTF8Encoding $false } catch { }
    $Ctx.PythonEnv = @{ PYTHONUTF8 = $env:PYTHONUTF8; PYTHONIOENCODING = $env:PYTHONIOENCODING }
    $env:PYTHONUTF8 = '1'
    $env:PYTHONIOENCODING = 'utf-8'
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    # Windows PowerShell 5.1 在从 PowerShell 7 中启动的程序（如 Python）里运行时继承了 PowerShell 7 的模块目录，
    # 会加载其中 PowerShell 7 的 Microsoft.PowerShell.Security 等模块而失败（Get-Acl、Set-Acl 不能用）
    if ($PSVersionTable.PSEdition -ne 'Core' -and $env:PSModulePath) {
        $Ctx.ModulePath = $env:PSModulePath
        $env:PSModulePath = @($env:PSModulePath -split ';' |
                Where-Object { $_ -and $_ -notmatch '(^|\\)PowerShell\\(\d[^\\]*\\)?Modules\\?$' }) -join ';'
    }
    Invoke-Install
} catch {
    $message = $_.Exception.Message
    if ($message -like 'TM_INSTALL_ABORT:*') {
        Write-Host ('install: ' + $message.Substring('TM_INSTALL_ABORT:'.Length)) -ForegroundColor Red
    } else {
        Write-Host "install: 出错了：$message" -ForegroundColor Red
        Write-Host $_.ScriptStackTrace
        $Ctx.ExitCode = 1
    }
    if ($Ctx.ExitCode -eq 0) { $Ctx.ExitCode = 1 }
} finally {
    foreach ($dir in $Ctx.TempDirs) { Remove-Item -LiteralPath $dir -Recurse -Force -ErrorAction SilentlyContinue }
    if ($null -ne $Ctx.ConsoleEncoding) { try { [Console]::OutputEncoding = $Ctx.ConsoleEncoding } catch { } }
    if ($Ctx.PythonEnv) { foreach ($k in $Ctx.PythonEnv.Keys) { [Environment]::SetEnvironmentVariable($k, $Ctx.PythonEnv[$k]) } }
    if ($null -ne $Ctx.ModulePath) { $env:PSModulePath = $Ctx.ModulePath }
}

# 用 -File 运行时以退出码报告结果；从平台用 [scriptblock]::Create 运行时设置 $LASTEXITCODE（不关闭窗口）。
if ($ScriptPath) { exit $Ctx.ExitCode }
$global:LASTEXITCODE = $Ctx.ExitCode
