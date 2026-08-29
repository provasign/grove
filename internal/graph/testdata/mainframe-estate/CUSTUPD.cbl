000100  IDENTIFICATION DIVISION.                                        ID73-80X
000200  PROGRAM-ID. CUSTUPD.                                            ID73-80X
000300* COMMENT WITH 05 FAKE-ITEM PIC X(10) THAT MUST NOT PARSE.        ID73-80X
000400  ENVIRONMENT DIVISION.                                           ID73-80X
000500      SELECT CUST-FILE ASSIGN TO CUSTIN.                          ID73-80X
000600  DATA DIVISION.                                                  ID73-80X
000700  FD  CUST-FILE.                                                  ID73-80X
000800  COPY CUSTREC.                                                   ID73-80X
000900  WORKING-STORAGE SECTION.                                        ID73-80X
001000  01  WS-FLAGS.                                                   ID73-80X
001100      05  WS-EOF      PIC X VALUE 'N'.                            ID73-80X
001200          88  END-OF-FILE VALUE 'Y'.                              ID73-80X
001300  77  WS-RPT-PGM  PIC X(8) VALUE 'CUSTRPT'.                       ID73-80X
001400  PROCEDURE DIVISION.                                             ID73-80X
001500  MAIN-PARA.                                                      ID73-80X
001600      PERFORM INIT-PARA THRU INIT-EXIT.                           ID73-80X
001700      CALL 'AUDITLOG' USING CUST-REC.                             ID73-80X
001800      CALL WS-RPT-PGM.                                            ID73-80X
001900      STOP RUN.                                                   ID73-80X
002000  INIT-PARA.                                                      ID73-80X
002100      MOVE 'N' TO WS-EOF.                                         ID73-80X
002200  INIT-EXIT.                                                      ID73-80X
002300      EXIT.                                                       ID73-80X
