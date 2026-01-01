Looking at "#### 3.4.6 Reading lossList for Retransmission (NAK received)" and the function nakLockedHonorOrder.  This is interesting becasue of the "i += 2".  The original code, before design_nak_btree.md, used to send a simplistic NAK that was always a range of the start sequence number and the end sequence number.  So the "i+=2", made sense because they came in pairs.

However, with the new nak btree, we store exactly the packets we need to NAK, and have have singles.  congestion/live/nak_consolidate.go consolidateNakBtree function, is supposed to generate 
```
Appendix A.  Packet Sequence List Coding

   For any single packet sequence number, it uses the original sequence
   number in the field.  The first bit MUST start with "0".




Sharabayko, et al.        Expires 11 March 2022                [Page 78]


Internet-Draft                     SRT                    September 2021


    0                   1                   2                   3
    0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |0|                   Sequence Number                           |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+

                 Figure 21: Single sequence numbers coding

   For any consecutive packet sequence numbers that the difference
   between the last and first is more than 1, only record the first (a)
   and the the last (b) sequence numbers in the list field, and modify
   the the first bit of a to "1".

    0                   1                   2                   3
    0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |1|                   Sequence Number a (first)                 |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
   |0|                   Sequence Number b (last)                  |
   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+

                Figure 22: Range of sequence numbers coding
```
