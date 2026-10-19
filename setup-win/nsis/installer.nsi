; RemoteDesk installer for Windows (NSIS; built on macOS by scripts/build.sh).
;
; One binary for every user: when it is downloaded the relay appends
; [config json]["RDCFG1"][uint32 LE length] (relay/server.js setupExe), and
; .onInit reads it back. The plain download link carries only the server: the
; "Mã truy cập" page asks for the code (checked with the relay), and that code
; decides which group of machines this one joins. Links with ?t= and the
; agent's remote update carry the code and skip the page. Running it over an older install (the PowerShell-based
; one or an earlier NSIS build) upgrades in place: the old agent is stopped and
; its task replaced, the machine id/name are kept. The agent's "update" command
; runs it with /S.

Unicode true
SetCompressor /SOLID lzma
!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "StrFunc.nsh"
!include "nsDialogs.nsh"
${StrStr}

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef OUTFILE
  !define OUTFILE "RemoteDesk-Setup-base.exe"
!endif
!ifndef PAYLOAD
  !define PAYLOAD "payload"
!endif
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\RemoteDesk"

Name "RemoteDesk"
OutFile "${OUTFILE}"
RequestExecutionLevel admin
ShowInstDetails nevershow
ShowUninstDetails nevershow
BrandingText "RemoteDesk ${VERSION}"
VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "RemoteDesk"
VIAddVersionKey "FileDescription" "RemoteDesk Setup"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "RemoteDesk"

Var CfgJson
Var HasToken ; the download carried an access code
Var Key      ; typed on the "Mã truy cập" page ("" = keep the current one)
Var KeyText

!define MUI_ABORTWARNING
!define MUI_COMPONENTSPAGE_NODESC
!define MUI_WELCOMEPAGE_TITLE "Cài đặt RemoteDesk"
!define MUI_WELCOMEPAGE_TEXT "RemoteDesk cho phép điều khiển máy tính này từ xa qua trang web.$\r$\n$\r$\nSau khi cài, máy sẽ tự hiện trong danh sách máy của tài khoản đã gửi bộ cài này cho bạn.$\r$\n$\r$\nBấm Tiến để tiếp tục."
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_COMPONENTS
Page custom KeyPage KeyPageLeave
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_TITLE "Đã cài xong RemoteDesk"
!define MUI_FINISHPAGE_TEXT "RemoteDesk đang chạy và sẽ tự chạy mỗi khi đăng nhập Windows.$\r$\n$\r$\nBạn có thể đóng cửa sổ này."
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "Vietnamese"

; ------------------------------------------------------------------ install

Section "-stop old" SecStop
  ; Upgrade in place: stop whatever runs from this folder so its files can be replaced.
  ; (Not "schtasks /End": during a remote update this installer may share the task's job.)
  nsExec::Exec 'taskkill /F /IM remote-agent.exe'
  Pop $0
  nsExec::Exec `powershell -NoProfile -Command "Get-Process ffmpeg -ErrorAction SilentlyContinue | Where-Object { $$_.Path -like '$INSTDIR\*' } | Stop-Process -Force"`
  Pop $0
  Sleep 800
SectionEnd

Section "RemoteDesk agent (để máy này được điều khiển)" SecAgent
  SectionIn RO
  SetOutPath "$INSTDIR"
  File "${PAYLOAD}\remote-agent.exe"
  File "${PAYLOAD}\ffmpeg.exe"
  File "${PAYLOAD}\register.ps1"

  ${If} $CfgJson != ""
    InitPluginsDir
    FileOpen $0 "$PLUGINSDIR\rdcfg.json" w
    FileWrite $0 $CfgJson
    FileClose $0
    FileOpen $0 "$PLUGINSDIR\rdkey.txt" w
    FileWrite $0 $Key
    FileClose $0
    ; The agent merges it into config.json, keeping this machine's id and name
    ; (and its access code when none was typed).
    nsExec::ExecToLog '"$INSTDIR\remote-agent.exe" -import-config "$PLUGINSDIR\rdcfg.json" -token-file "$PLUGINSDIR\rdkey.txt"'
    Pop $0
    ${If} $0 != 0
      MessageBox MB_ICONSTOP "Không ghi được cấu hình RemoteDesk (mã lỗi $0). Nếu đây là lần cài đầu, hãy nhập mã truy cập." /SD IDOK
      Abort
    ${EndIf}
  ${EndIf}

  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayName" "RemoteDesk"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINST_KEY}" "Publisher" "RemoteDesk"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\remote-agent.exe"
  WriteRegStr HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKLM "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoRepair" 1
SectionEnd

Section /o "App RemoteDesk (để từ máy này điều khiển máy khác)" SecApp
  SetOutPath "$INSTDIR"
  File "${PAYLOAD}\RemoteDesk.exe"
  CreateShortCut "$SMPROGRAMS\RemoteDesk.lnk" "$INSTDIR\RemoteDesk.exe"
  CreateShortCut "$DESKTOP\RemoteDesk.lnk" "$INSTDIR\RemoteDesk.exe"
SectionEnd

Section "-register" SecRegister
  ; Logon task (highest privileges), config ACL, firewall rule for P2P, no sleep on AC; starts the agent.
  nsExec::ExecToLog 'powershell -NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\register.ps1" -Dir "$INSTDIR"'
  Pop $0
  ${If} $0 != 0
    MessageBox MB_ICONEXCLAMATION "Không đăng ký được tác vụ tự chạy (mã lỗi $0). Hãy chạy lại bộ cài." /SD IDOK
  ${EndIf}
  ; Left behind by a remote update (the agent runs this installer as a one-shot task).
  nsExec::Exec 'schtasks /Delete /TN RemoteDesk-Update /F'
  Pop $0
SectionEnd

; ------------------------------------------------------------------ access code

Function KeyPage
  ${If} $HasToken == 1
    Abort ; the link carried the code
  ${EndIf}
  !insertmacro MUI_HEADER_TEXT "Mã truy cập" "Máy này sẽ vào nhóm máy của mã bạn nhập."
  nsDialogs::Create 1018
  Pop $0
  ${NSD_CreateLabel} 0 0 100% 30u "Nhập mã truy cập do người quản trị RemoteDesk gửi cho bạn. Ai đăng nhập trang web bằng mã này sẽ xem và điều khiển được máy này."
  Pop $0
  ${NSD_CreateText} 0 34u 100% 13u "$Key"
  Pop $KeyText
  ${If} ${FileExists} "$INSTDIR\config.json"
    ${NSD_CreateLabel} 0 54u 100% 30u "Máy này đã cài RemoteDesk. Để trống để giữ nhóm máy hiện tại; nhập mã khác để chuyển máy sang nhóm của mã đó."
    Pop $0
  ${EndIf}
  ${NSD_SetFocus} $KeyText
  nsDialogs::Show
FunctionEnd

; Checks the code with the relay before installing anything.
Function KeyPageLeave
  ${NSD_GetText} $KeyText $Key
  ${If} $Key == ""
    ${If} ${FileExists} "$INSTDIR\config.json"
      Return
    ${EndIf}
    MessageBox MB_ICONEXCLAMATION "Hãy nhập mã truy cập."
    Abort
  ${EndIf}
  InitPluginsDir
  File "/oname=$PLUGINSDIR\rd-check.exe" "${PAYLOAD}\remote-agent.exe"
  FileOpen $0 "$PLUGINSDIR\rdcfg.json" w
  FileWrite $0 $CfgJson
  FileClose $0
  FileOpen $0 "$PLUGINSDIR\rdkey.txt" w
  FileWrite $0 $Key
  FileClose $0
  nsExec::Exec '"$PLUGINSDIR\rd-check.exe" -check-token "$PLUGINSDIR\rdcfg.json" -token-file "$PLUGINSDIR\rdkey.txt"'
  Pop $0
  ${If} $0 == 3
    MessageBox MB_ICONEXCLAMATION "Mã truy cập không đúng hoặc đã bị khoá. Kiểm tra lại mã."
    Abort
  ${ElseIf} $0 != 0
    MessageBox MB_YESNO|MB_ICONQUESTION "Không kiểm tra được mã (máy chưa có mạng?). Vẫn cài với mã này?" IDYES keep
    Abort
    keep:
  ${EndIf}
FunctionEnd

; ------------------------------------------------------------------ init

; Reads the config the relay appended: [json]["RDCFG1"][uint32 LE length].
Function ReadTrailer
  StrCpy $CfgJson ""
  ClearErrors
  FileOpen $0 "$EXEPATH" r
  IfErrors done
  FileSeek $0 -10 END
  StrCpy $1 ""
  ${For} $2 1 6
    FileReadByte $0 $3
    IntFmt $3 "%c" $3
    StrCpy $1 "$1$3"
  ${Next}
  ${If} $1 S== "RDCFG1"
    FileReadByte $0 $4
    FileReadByte $0 $5
    FileReadByte $0 $6
    FileReadByte $0 $7
    IntOp $5 $5 << 8
    IntOp $6 $6 << 16
    IntOp $7 $7 << 24
    IntOp $4 $4 + $5
    IntOp $4 $4 + $6
    IntOp $4 $4 + $7
    ${If} $4 > 0
    ${AndIf} $4 < 1000
      IntOp $5 $4 + 10
      IntOp $5 0 - $5
      FileSeek $0 $5 END
      FileRead $0 $CfgJson $4
    ${EndIf}
  ${EndIf}
  FileClose $0
  done:
FunctionEnd

Function .onInit
  SetShellVarContext all
  StrCpy $INSTDIR "$APPDATA\RemoteDesk" ; C:\ProgramData\RemoteDesk
  Call ReadTrailer
  StrCpy $HasToken 0
  ${StrStr} $0 $CfgJson '"token":"'
  ${If} $0 != ""
    StrCpy $HasToken 1
  ${EndIf}
  ${If} $CfgJson == ""
  ${AndIfNot} ${FileExists} "$INSTDIR\config.json"
    MessageBox MB_ICONSTOP "Bộ cài này không đúng.$\r$\nHãy tải lại từ trang RemoteDesk." /SD IDOK
    Abort
  ${EndIf}
  ; Agent + app variant, or the app is already installed: keep it.
  ${StrStr} $0 $CfgJson '"app":true'
  ${If} $0 != ""
  ${OrIf} ${FileExists} "$INSTDIR\RemoteDesk.exe"
    SectionSetFlags ${SecApp} ${SF_SELECTED}
  ${EndIf}
FunctionEnd

; ------------------------------------------------------------------ uninstall

Section "Uninstall"
  SetShellVarContext all
  nsExec::Exec 'schtasks /End /TN RemoteDesk'
  Pop $0
  nsExec::Exec 'schtasks /Delete /TN RemoteDesk /F'
  Pop $0
  nsExec::Exec 'taskkill /F /IM remote-agent.exe'
  Pop $0
  nsExec::Exec 'netsh advfirewall firewall delete rule name="RemoteDesk agent"'
  Pop $0
  Sleep 800
  Delete "$SMPROGRAMS\RemoteDesk.lnk"
  Delete "$DESKTOP\RemoteDesk.lnk"
  RMDir /r "$INSTDIR"
  DeleteRegKey HKLM "${UNINST_KEY}"
SectionEnd

Function un.onInit
  SetShellVarContext all
FunctionEnd
