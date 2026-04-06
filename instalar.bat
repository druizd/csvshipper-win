@echo off
echo ===================================================
echo INSTALADOR PLUG AND PLAY - SQL SHIPPER (WINDOWS)
echo ===================================================
echo.
echo 1. Instalando el servicio nativo de Windows...
sqlshipper.exe -install
echo.
echo 2. Iniciando el servicio en segundo plano...
sqlshipper.exe -start
echo.
echo ===================================================
echo ¡LISTO! El servicio ya esta corriendo. 
echo Puedes ver los logs de error en: C:\Windows\Temp\sqlshipper.log
echo ===================================================
pause
