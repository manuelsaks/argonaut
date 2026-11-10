Add-Type -AssemblyName System.Drawing
$tuiPalette = @('#000000','#aa0000','#00aa00','#aa5500','#0000aa','#aa00aa','#00aaaa','#aaaaaa','#555555','#ff5555','#55ff55','#ffff55','#5555ff','#ff55ff','#55ffff','#ffffff') | ForEach-Object { [System.Drawing.ColorTranslator]::FromHtml($_) }
function Get-TuiColor([int]$number) {
    if ($number -lt 16) { return $tuiPalette[$number] }
    if ($number -lt 232) {
        $n=$number-16; $v=@(0,95,135,175,215,255)
        return [System.Drawing.Color]::FromArgb($v[[int][math]::Floor($n/36)],$v[[int][math]::Floor(($n%36)/6)],$v[$n%6])
    }
    $shade=8+10*($number-232)
    return [System.Drawing.Color]::FromArgb($shade,$shade,$shade)
}
$tuiFont = [System.Drawing.Font]::new('Consolas',15,[System.Drawing.FontStyle]::Regular,[System.Drawing.GraphicsUnit]::Pixel)
$tuiBoldFont = [System.Drawing.Font]::new('Consolas',15,[System.Drawing.FontStyle]::Bold,[System.Drawing.GraphicsUnit]::Pixel)
foreach ($tuiSpec in @(@('argo',120,30),@('popup',120,30),@('popup',60,20),@('argo',160,40))) {
    $tuiName=$tuiSpec[0]; $tuiCols=[int]$tuiSpec[1]; $tuiRows=[int]$tuiSpec[2]
    $tuiData=Get-Content -LiteralPath ('.audit/k9s-'+$tuiName+'-'+$tuiCols+'x'+$tuiRows+'.ansi') -Raw -Encoding utf8
    $tuiBitmap=[System.Drawing.Bitmap]::new($tuiCols*9+20,$tuiRows*19+20)
    $tuiGraphics=[System.Drawing.Graphics]::FromImage($tuiBitmap)
    $tuiGraphics.Clear([System.Drawing.Color]::Black)
    $tuiGraphics.TextRenderingHint=[System.Drawing.Text.TextRenderingHint]::SingleBitPerPixelGridFit
    $tuiFg=$tuiPalette[7]; $tuiBg=$tuiPalette[0]; $tuiStrong=$false; $tuiUnderline=$false; $tuiReverse=$false; $tuiX=0; $tuiY=0
    $tuiPattern='('+[char]27+'\[[0-9;:]*m)'
    foreach ($tuiToken in [regex]::Split($tuiData,$tuiPattern)) {
        if ($tuiToken.StartsWith([string][char]27+'[')) {
            $tuiCodes=@($tuiToken.Substring(2,$tuiToken.Length-3).Replace(':',';').Split(';') | ForEach-Object { if ($_ -eq '') {0} else {[int]$_} })
            for ($tuiI=0; $tuiI -lt $tuiCodes.Length; $tuiI++) {
                $tuiN=$tuiCodes[$tuiI]
                if ($tuiN -eq 0) {$tuiFg=$tuiPalette[7];$tuiBg=$tuiPalette[0];$tuiStrong=$false;$tuiUnderline=$false;$tuiReverse=$false}
                elseif ($tuiN -eq 1) {$tuiStrong=$true} elseif ($tuiN -eq 22) {$tuiStrong=$false}
                elseif ($tuiN -eq 4) {$tuiUnderline=$true} elseif ($tuiN -eq 24) {$tuiUnderline=$false}
                elseif ($tuiN -eq 7) {$tuiReverse=$true} elseif ($tuiN -eq 27) {$tuiReverse=$false}
                elseif ($tuiN -eq 39) {$tuiFg=$tuiPalette[7]} elseif ($tuiN -eq 49) {$tuiBg=$tuiPalette[0]}
                elseif ($tuiN -ge 30 -and $tuiN -le 37) {$tuiFg=$tuiPalette[$tuiN-30]}
                elseif ($tuiN -ge 90 -and $tuiN -le 97) {$tuiFg=$tuiPalette[$tuiN-90+8]}
                elseif ($tuiN -ge 40 -and $tuiN -le 47) {$tuiBg=$tuiPalette[$tuiN-40]}
                elseif ($tuiN -ge 100 -and $tuiN -le 107) {$tuiBg=$tuiPalette[$tuiN-100+8]}
                elseif (($tuiN -eq 38 -or $tuiN -eq 48) -and $tuiI+2 -lt $tuiCodes.Length) {
                    $tuiColor=$null
                    if ($tuiCodes[$tuiI+1] -eq 5) {$tuiColor=Get-TuiColor $tuiCodes[$tuiI+2];$tuiI+=2}
                    elseif ($tuiCodes[$tuiI+1] -eq 2 -and $tuiI+4 -lt $tuiCodes.Length) {$tuiColor=[System.Drawing.Color]::FromArgb($tuiCodes[$tuiI+2],$tuiCodes[$tuiI+3],$tuiCodes[$tuiI+4]);$tuiI+=4}
                    if ($null -ne $tuiColor) {if ($tuiN -eq 38) {$tuiFg=$tuiColor} else {$tuiBg=$tuiColor}}
                }
            }
        } else {
            foreach ($tuiChar in $tuiToken.ToCharArray()) {
                if ($tuiChar -eq [char]10) {$tuiY++;$tuiX=0;continue}
                if ($tuiChar -eq [char]13) {continue}
                if ($tuiX -lt $tuiCols -and $tuiY -lt $tuiRows) {
                    $tuiTextColor=$tuiFg; $tuiCellColor=$tuiBg
                    if ($tuiReverse) {$tuiTextColor=$tuiBg;$tuiCellColor=$tuiFg}
                    $tuiBrush=[System.Drawing.SolidBrush]::new($tuiCellColor)
                    $tuiTextBrush=[System.Drawing.SolidBrush]::new($tuiTextColor)
                    $tuiPx=10+$tuiX*9; $tuiPy=10+$tuiY*19
                    $tuiGraphics.FillRectangle($tuiBrush,$tuiPx,$tuiPy,9,19)
                    $tuiChosenFont=$tuiFont
                    if ($tuiStrong) {$tuiChosenFont=$tuiBoldFont}
                    $tuiGraphics.DrawString([string]$tuiChar,$tuiChosenFont,$tuiTextBrush,[single]$tuiPx,[single]$tuiPy,[System.Drawing.StringFormat]::GenericTypographic)
                    if ($tuiUnderline) {$tuiPen=[System.Drawing.Pen]::new($tuiTextColor);$tuiGraphics.DrawLine($tuiPen,$tuiPx,$tuiPy+17,$tuiPx+8,$tuiPy+17);$tuiPen.Dispose()}
                    $tuiBrush.Dispose();$tuiTextBrush.Dispose()
                }
                $tuiX++
            }
        }
    }
    $tuiTarget=[System.IO.Path]::GetFullPath('.audit/k9s-'+$tuiName+'-'+$tuiCols+'x'+$tuiRows+'.png')
    $tuiBitmap.Save($tuiTarget,[System.Drawing.Imaging.ImageFormat]::Png)
    $tuiGraphics.Dispose();$tuiBitmap.Dispose()
}
$tuiFont.Dispose();$tuiBoldFont.Dispose()
