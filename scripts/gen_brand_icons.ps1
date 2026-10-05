# gen_brand_icons.ps1 — Limulus · 鲎 品牌资产生成器
# 单一真源：几何定义在下方 $GeoA/$GeoMini，脚本据此
#   1) 写出矢量母版 assets/brand/limulus.svg / limulus-mini.svg
#   2) WPF 光栅化全部 PNG + 多尺寸 PNG-in-ICO
# 零外部依赖（仅 .NET WPF）。在仓库根运行：powershell -File scripts/gen_brand_icons.ps1
param(
    [string]$RepoRoot = (Split-Path $PSScriptRoot -Parent)
)
$ErrorActionPreference = 'Stop'

if ([System.Threading.Thread]::CurrentThread.GetApartmentState() -ne 'STA') {
    # WPF RenderTargetBitmap 需要 STA
    powershell -NoProfile -STA -File $MyInvocation.MyCommand.Path -RepoRoot $RepoRoot
    exit $LASTEXITCODE
}

Add-Type -AssemblyName PresentationCore, WindowsBase

# ============================================================
# 几何定义（512 画布，鲎头朝上）—— 改这里即可全量重生成
# ============================================================
$Grad = @{
    Start = '80,60'      # userSpaceOnUse 起点与终点
    End   = '440,460'
    From  = '#38BDF8'    # 天青（鲎蓝之浅）
    To    = '#0C4A6E'    # 深鲎蓝
}

$GeoA = @{                       # 变体 A 全量版：穹顶+腹甲+侧刺+尾剑+心区脊+复眼
    SquircleRx = 116
    Prosoma    = 'M 256 94 C 168 94 102 160 102 250 C 102 279 116 298 146 298 L 366 298 C 396 298 410 279 410 250 C 410 160 344 94 256 94 Z'
    Ridge      = 'M 150 240 Q 256 286 362 240'
    Opistho    = 'M 172 314 L 340 314 L 352 358 Q 354 374 338 374 L 174 374 Q 158 374 160 358 Z'
    SpineL     = 'M 162 330 L 130 347 L 165 354 Z'
    SpineR     = 'M 350 330 L 382 347 L 347 354 Z'
    Telson     = 'M 236 388 L 276 388 C 270 416 264 442 256 464 C 248 442 242 416 236 388 Z'
    Eyes       = @( @(216, 132), @(296, 132) )
    EyeR       = 11
}

$GeoMini = @{                    # ≤20px 简化版：穹顶+粗尾剑+复眼（细节全舍弃）
    SquircleRx = 116
    Prosoma    = 'M 256 108 C 170 108 106 172 106 254 C 106 284 122 302 152 302 L 360 302 C 390 302 406 284 406 254 C 406 172 342 108 256 108 Z'
    Telson     = 'M 230 386 L 282 386 L 266 462 Q 256 478 246 462 Z'
    Eyes       = @( @(216, 140), @(296, 140) )
    EyeR       = 12
}

$CrabWhite = '#F8FAFC'
$EyeCopper = '#FB923C'

# ============================================================
# SVG 母版写出
# ============================================================
function Build-Svg([hashtable]$Geo) {
    $sb = [System.Text.StringBuilder]::new()
    [void]$sb.AppendLine('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">')
    [void]$sb.AppendLine('  <defs>')
    [void]$sb.AppendLine("    <linearGradient id=`"bg`" x1=`"$($Grad.Start.Split(',')[0])`" y1=`"$($Grad.Start.Split(',')[1])`" x2=`"$($Grad.End.Split(',')[0])`" y2=`"$($Grad.End.Split(',')[1])`" gradientUnits=`"userSpaceOnUse`">")
    [void]$sb.AppendLine("      <stop offset=`"0`" stop-color=`"$($Grad.From)`"/>")
    [void]$sb.AppendLine("      <stop offset=`"1`" stop-color=`"$($Grad.To)`"/>")
    [void]$sb.AppendLine('    </linearGradient>')
    [void]$sb.AppendLine('  </defs>')
    [void]$sb.AppendLine("  <rect width=`"512`" height=`"512`" rx=`"$($Geo.SquircleRx)`" fill=`"url(#bg)`"/>")
    [void]$sb.AppendLine("  <g fill=`"$CrabWhite`">")
    [void]$sb.AppendLine("    <path d=`"$($Geo.Prosoma)`"/>")
    if ($Geo.Opistho) {
        [void]$sb.AppendLine("    <path d=`"$($Geo.Opistho)`"/>")
        [void]$sb.AppendLine("    <path d=`"$($Geo.SpineL)`"/>")
        [void]$sb.AppendLine("    <path d=`"$($Geo.SpineR)`"/>")
    }
    [void]$sb.AppendLine("    <path d=`"$($Geo.Telson)`"/>")
    [void]$sb.AppendLine('  </g>')
    if ($Geo.Ridge) {
        # 负空间心区脊：用背景渐变描边，与底色逐像素对齐
        [void]$sb.AppendLine("  <path d=`"$($Geo.Ridge)`" fill=`"none`" stroke=`"url(#bg)`" stroke-width=`"13`" stroke-linecap=`"round`"/>")
    }
    foreach ($e in $Geo.Eyes) {
        [void]$sb.AppendLine("  <circle cx=`"$($e[0])`" cy=`"$($e[1])`" r=`"$($Geo.EyeR)`" fill=`"$EyeCopper`"/>")
    }
    [void]$sb.AppendLine('</svg>')
    $sb.ToString()
}

$brandDir = Join-Path $RepoRoot 'assets/brand'
New-Item -ItemType Directory -Force -Path $brandDir | Out-Null
[IO.File]::WriteAllText((Join-Path $brandDir 'limulus.svg'), (Build-Svg $GeoA))
[IO.File]::WriteAllText((Join-Path $brandDir 'limulus-mini.svg'), (Build-Svg $GeoMini))
Write-Host "SVG 母版 → assets/brand/limulus.svg, limulus-mini.svg"

# ============================================================
# WPF 光栅化
# ============================================================
function New-BgBrush {
    $b = [System.Windows.Media.LinearGradientBrush]::new()
    $b.MappingMode = [System.Windows.Media.BrushMappingMode]::Absolute
    $s, $e2 = $Grad.Start.Split(','), $Grad.End.Split(',')
    $b.StartPoint = [System.Windows.Point]::new([double]$s[0], [double]$s[1])
    $b.EndPoint   = [System.Windows.Point]::new([double]$e2[0], [double]$e2[1])
    $b.GradientStops.Add([System.Windows.Media.GradientStop]::new([System.Windows.Media.ColorConverter]::ConvertFromString($Grad.From), 0.0))
    $b.GradientStops.Add([System.Windows.Media.GradientStop]::new([System.Windows.Media.ColorConverter]::ConvertFromString($Grad.To), 1.0))
    $b.Freeze()
    return $b
}

function New-Visual([hashtable]$Geo, [double]$Scale = 1.0) {
    # 返回 512 逻辑坐标下的 DrawingVisual；Scale 用于整体缩放（logo_with_text 场景）
    $dv = [System.Windows.Media.DrawingVisual]::new()
    $dc = $dv.RenderOpen()
    $bg = New-BgBrush
    $white = [System.Windows.Media.SolidColorBrush]::new([System.Windows.Media.ColorConverter]::ConvertFromString($CrabWhite))
    $white.Freeze()
    $copper = [System.Windows.Media.SolidColorBrush]::new([System.Windows.Media.ColorConverter]::ConvertFromString($EyeCopper))
    $copper.Freeze()

    if ($Scale -ne 1.0) {
        $dc.PushTransform([System.Windows.Media.ScaleTransform]::new($Scale, $Scale))
    }
    $dc.DrawGeometry($bg, $null, [System.Windows.Media.RectangleGeometry]::new(
        [System.Windows.Rect]::new(0, 0, 512, 512), $Geo.SquircleRx, $Geo.SquircleRx))
    $pen = $null
    foreach ($d in @($Geo.Prosoma, $Geo.Opistho, $Geo.SpineL, $Geo.SpineR, $Geo.Telson)) {
        if ($d) { $dc.DrawGeometry($white, $null, [System.Windows.Media.Geometry]::Parse($d)) }
    }
    if ($Geo.Ridge) {
        # 负空间脊线：与背景同一把渐变刷（Absolute 映射），无缝
        $rp = [System.Windows.Media.Geometry]::Parse($Geo.Ridge)
        $bgPen = [System.Windows.Media.Pen]::new((New-BgBrush), 13)
        $bgPen.StartLineCap = [System.Windows.Media.PenLineCap]::Round
        $bgPen.EndLineCap = [System.Windows.Media.PenLineCap]::Round
        $bgPen.Freeze()
        $dc.DrawGeometry($null, $bgPen, $rp)
    }
    foreach ($e in $Geo.Eyes) {
        $dc.DrawGeometry($copper, $null, [System.Windows.Media.EllipseGeometry]::new(
            [System.Windows.Point]::new($e[0], $e[1]), $Geo.EyeR, $Geo.EyeR))
    }
    if ($Scale -ne 1.0) { $dc.Pop() }
    $dc.Close()
    return $dv
}

function Render-Png([hashtable]$Geo, [int]$Px, [string]$OutPath, [double]$Scale = 1.0) {
    $dv = New-Visual $Geo $Scale
    $rtb = New-RtbFromVisual $dv $Px
    $enc = [System.Windows.Media.Imaging.PngBitmapEncoder]::new()
    $enc.Frames.Add([System.Windows.Media.Imaging.BitmapFrame]::Create($rtb))
    $fs = [System.IO.File]::Create($OutPath)
    $enc.Save($fs); $fs.Close()
    Write-Host ("PNG {0,4}px  → {1}" -f $Px, (Resolve-Path $OutPath -Relative))
}

# 关键：RenderTargetBitmap 的 DPI 语义 —— 像素 = 逻辑单位 × dpi/96。
# 512 单位的画布要压进 Px 像素，dpi 必须是 96×Px/512；
# 否则 RTB(Px,Px,96,96) 只会渲染左上角 Px×Px 区域（裁剪而非缩放）。
function New-RtbFromVisual([System.Windows.Media.DrawingVisual]$dv, [int]$Px) {
    $dpi = 96.0 * $Px / 512.0
    $rtb = [System.Windows.Media.Imaging.RenderTargetBitmap]::new($Px, $Px, $dpi, $dpi, [System.Windows.Media.PixelFormats]::Pbgra32)
    $rtb.Render($dv)
    return $rtb
}

function Get-PngBytes([System.Windows.Media.Imaging.RenderTargetBitmap]$rtb) {
    $enc = [System.Windows.Media.Imaging.PngBitmapEncoder]::new()
    $enc.Frames.Add([System.Windows.Media.Imaging.BitmapFrame]::Create($rtb))
    $ms = [System.IO.MemoryStream]::new()
    $enc.Save($ms)
    return , $ms.ToArray()  # 逗号包裹：避免 PS 管道枚举 byte[]，调用方拿到完整的 byte[]
}

function New-Ico([object[]]$Entries, [string]$OutPath) {
    # PNG-in-ICO（Vista+ 全支持）。Entries: @{Px=int; Rtb=RenderTargetBitmap}
    $ms = [System.IO.MemoryStream]::new()
    $bw = [System.IO.BinaryWriter]::new($ms)
    $bw.Write([uint16]0); $bw.Write([uint16]1); $bw.Write([uint16]$Entries.Count)
    $offset = 6 + 16 * $Entries.Count
    $blobs = @()
    foreach ($e in $Entries) {
        $png = Get-PngBytes $e.Rtb
        $blobs += , $png
        $dim = if ($e.Px -ge 256) { 0 } else { [byte]$e.Px }
        $bw.Write([byte]$dim); $bw.Write([byte]$dim); $bw.Write([byte]0); $bw.Write([byte]0)
        $bw.Write([uint16]1); $bw.Write([uint16]32)
        $bw.Write([uint32]$png.Length); $bw.Write([uint32]$offset)
        $offset += $png.Length
    }
    foreach ($b in $blobs) { $bw.Write($b) }
    $bw.Flush()
    [IO.File]::WriteAllBytes($OutPath, $ms.ToArray())
    Write-Host ("ICO {0,12} → {1}" -f (($Entries | ForEach-Object { $_.Px }) -join '/'), (Resolve-Path $OutPath -Relative))
}

function New-Rtb([hashtable]$Geo, [int]$Px) {
    return (New-RtbFromVisual (New-Visual $Geo) $Px)
}

# ============================================================
# 输出：web 前端 + 后端托盘/EXE 图标
# ============================================================
$fePub = Join-Path $RepoRoot 'web/frontend/public'
$beDir = Join-Path $RepoRoot 'web/backend'

Render-Png $GeoA 96  (Join-Path $fePub 'favicon-96x96.png')
Render-Png $GeoA 180 (Join-Path $fePub 'apple-touch-icon.png')
Render-Png $GeoA 192 (Join-Path $fePub 'web-app-manifest-192x192.png')
Render-Png $GeoA 512 (Join-Path $fePub 'web-app-manifest-512x512.png')
Render-Png $GeoA 512 (Join-Path $beDir 'icon.png')

# favicon.ico：16/20 用 mini，其余全量
New-Ico @(
    @{ Px = 16;  Rtb = (New-Rtb $GeoMini 16) }
    @{ Px = 32;  Rtb = (New-Rtb $GeoA 32) }
    @{ Px = 48;  Rtb = (New-Rtb $GeoA 48) }
    @{ Px = 64;  Rtb = (New-Rtb $GeoA 64) }
    @{ Px = 128; Rtb = (New-Rtb $GeoA 128) }
    @{ Px = 256; Rtb = (New-Rtb $GeoA 256) }
) (Join-Path $fePub 'favicon.ico')

# Windows 托盘 + EXE 资源 icon.ico（多带一个 24 给资源管理器列表视图）
New-Ico @(
    @{ Px = 16;  Rtb = (New-Rtb $GeoMini 16) }
    @{ Px = 24;  Rtb = (New-Rtb $GeoMini 24) }
    @{ Px = 32;  Rtb = (New-Rtb $GeoA 32) }
    @{ Px = 48;  Rtb = (New-Rtb $GeoA 48) }
    @{ Px = 64;  Rtb = (New-Rtb $GeoA 64) }
    @{ Px = 128; Rtb = (New-Rtb $GeoA 128) }
    @{ Px = 256; Rtb = (New-Rtb $GeoA 256) }
) (Join-Path $beDir 'icon.ico')

# ============================================================
# logo_with_text.png（web 头部横版标，500×104 透明底）
# ============================================================
{
    $w, $h = 500, 104
    $dv = [System.Windows.Media.DrawingVisual]::new()
    $dc = $dv.RenderOpen()
    # 白色版标志（透明底）：用 mini 几何在 104×104 内自绘
    $white = [System.Windows.Media.SolidColorBrush]::new([System.Windows.Media.ColorConverter]::ConvertFromString($CrabWhite))
    $white.Freeze()
    $copper = [System.Windows.Media.SolidColorBrush]::new([System.Windows.Media.ColorConverter]::ConvertFromString($EyeCopper))
    $copper.Freeze()
    $dc.PushTransform([System.Windows.Media.TranslateTransform]::new(0, 0))
    $dc.PushTransform([System.Windows.Media.ScaleTransform]::new($h / 512, $h / 512))
    $dc.DrawGeometry($white, $null, [System.Windows.Media.Geometry]::Parse($GeoA.Prosoma))
    $dc.DrawGeometry($white, $null, [System.Windows.Media.Geometry]::Parse($GeoA.Opistho))
    $dc.DrawGeometry($white, $null, [System.Windows.Media.Geometry]::Parse($GeoA.SpineL))
    $dc.DrawGeometry($white, $null, [System.Windows.Media.Geometry]::Parse($GeoA.SpineR))
    $dc.DrawGeometry($white, $null, [System.Windows.Media.Geometry]::Parse($GeoA.Telson))
    foreach ($e in $GeoA.Eyes) {
        $dc.DrawGeometry($copper, $null, [System.Windows.Media.EllipseGeometry]::new(
            [System.Windows.Point]::new($e[0], $e[1]), $GeoA.EyeR, $GeoA.EyeR))
    }
    $dc.Pop(); $dc.Pop()
    # 文字
    $tf = [System.Windows.Media.Typeface]::new([System.Windows.Media.FontFamily]::new('Segoe UI'), [System.Windows.FontStyles]::Normal, [System.Windows.FontWeights]::Bold, [System.Windows.FontStretches]::Normal)
    $ft = [System.Windows.Media.FormattedText]::new('LIMULUS', [Globalization.CultureInfo]::InvariantCulture,
        [System.Windows.FlowDirection]::LeftToRight, $tf, 40, $white, 1.0)
    $baselineY = ($h + $ft.Height) / 2
    $dc.DrawText($ft, [System.Windows.Point]::new($h + 22, $baselineY - $ft.Height))
    $dc.Close()
    $rtb = [System.Windows.Media.Imaging.RenderTargetBitmap]::new($w, $h, 96, 96, [System.Windows.Media.PixelFormats]::Pbgra32)
    $rtb.Render($dv)
    $enc = [System.Windows.Media.Imaging.PngBitmapEncoder]::new()
    $enc.Frames.Add([System.Windows.Media.Imaging.BitmapFrame]::Create($rtb))
    $fs = [System.IO.File]::Create((Join-Path $fePub 'logo_with_text.png'))
    $enc.Save($fs); $fs.Close()
    Write-Host "PNG  500x104 → logo_with_text.png（头部横版标）"
}.Invoke()

# favicon.svg = 矢量全量版（真实矢量，替换原 base64 位图包装）
[IO.File]::WriteAllText((Join-Path $fePub 'favicon.svg'), (Build-Svg $GeoA))
Write-Host 'SVG 矢量   → favicon.svg'

Write-Host "`n完成。前端 dist 需重新构建：cd web/frontend && pnpm build:backend"
