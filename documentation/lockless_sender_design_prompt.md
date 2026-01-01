Ok, we have been doing a lot of good planning in retransmission_and_nak_suppression_design.md, but I think we need to consider something more radical.

I'd like to create a new design called lockless_sender_design.md, in which we expand on the ideas in retransmission_and_nak_suppression_design.md, except that we are going to think bigger and design a lock free sender, using an event loop.

The big advantage will be that currently packets arrive with spacing between the packets, but because of the Tick(), we will end up batching the packets into a burst.  This is really bad for the network, and is probably causing more losses that are required.  However there are other parts of the design that can be optimized.

The gosrt_lockless_design.md is a great refernce for how we did this for the receiver side, and we can follow a lot of the same patterns.

Here's roughly the plan I'm thinking about.

In section "#### 3.4.1 Packet Lifecycle State Machine", we currently Push(p) to the packetList.  I'm thinking that instead of this we can have another lock free ring, SendPacketRing.  This will need to have a single shard, because we want to preserve order.  This will mean the Push(p) won't need a lock and will be very fast.

Then and this is also like the reciever, we have a single consumer that will read from the SendPacketRing, and InsertOrReplace into a sendPacketBtree using the same style btree we use for the (receiver) packet btree.  The btree might seem like over kill for an order list, but the reason is that in nakLockedHonorOrder(), the current "for e := s.lossList.Front(); e != nil; e = e.Next()" is very inefficient.  The other advantage of the sendPacketBtree, is that we won't need to move packets from the packetList to the lossList.  Instead, packets enter the sendPacketBtree, and can stay in the same ordered btree the entire time.

There are essentially x2 points we need to track in the position of the btree, and one of these is the .Min() point:
a)
When we receive the ACKs from the reciever, this is telling us were the reciever has contigious packets up to.  We can perform the btree SendPacketRing .DeleteMin() until we have deleted all the packets the receiver has confirmed it has recieved.  This will be high performance.
b)
The other point is to be use for scanning the btree to look for packets to deliver.  This will be the DeliveryStartPoint.  When the first packet is inserted into the sendPacketBtree, we will set the DeliveryStartPoint will be set to this point.  When we need to run the deliver packets function, it will start from DeliveryScanPoint and scan forward while PktTsbpdTime <= now, delivering any packets that are ready to be delivered.  It will record the point it was up to DeliveryStartPoint, so that the scanning will be efficient.

When any NAKs arrive, looking up each NAK entry in the NAK packet will use the btree lookup, so it will be fast.

```
                        TSBPD Timeline
    ───────────────────────────────────────────────────►

    │◄──────────── tsbpdDelay (e.g., 3000ms) ─────────►│
    │                                                  │
    │       DeliveryStartPoint                    now+tsbpdDelay
    │              │                                   │
    │              ▼                                   ▼
    ├──────────────┬───────────────────────────────────┤
    │ Waiting for  │       SCAN THIS RANGE             │
    │ ACK          │                                   │
    ├──────────────┴───────────────────────────────────┤


    Example with tsbpdDelay = 3000ms:
    - ACKs cause the .DeleteMin()
    - DeliveryStartPoint used to scan for packets to deliver
    - nakLockedHonorOrder() can retranmit from this btree efficiently
```

For a memory reuse/lifetime perspective, we are going to want to use the same ideas as we did for the io_uring and packet btree.  
