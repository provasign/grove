000100* SHARED CUSTOMER RECORD                                          ID73-80X
000200  01  CUST-REC.                                                   ID73-80X
000300      05  CUST-KEY.                                               ID73-80X
000400          10  CUST-ID    PIC 9(8).                                ID73-80X
000500      05  CUST-SSN   PIC 9(9).                                    ID73-80X
000600      05  CUST-ALT REDEFINES CUST-SSN PIC X(9).                   ID73-80X
000700      05  CUST-ORDERS OCCURS 10 PIC 9(4).                         ID73-80X
