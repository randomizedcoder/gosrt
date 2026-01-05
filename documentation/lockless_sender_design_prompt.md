Ok, we have been doing a lot of good planning in retransmission_and_nak_suppression_design.md, but I think we need to consider something more radical.

I'd like to create a new design called lockless_sender_design.md, in which we expand on the ideas in retransmission_and_nak_suppression_design.md, except that we are going to think bigger and design a lock free sender, using an event loop.  "### 3.3 Concurrency Protection and Lockless Design" does a good job summarizing the receiver lock free design, and section "### 3.4 Sender Lock Architecture" described the current sender locking, which we want to continue to support, but also support a new lock free method.  

This new design doc should refer to the other design docs, and key sections.

The big advantage will be that currently packets arrive with spacing between the packets, but because of the Tick(), we will end up batching the packets into a burst.  This is really bad for the network, and is probably causing more losses that are required.  However there are other parts of the design that can be optimized.

The gosrt_lockless_design.md is a great refernce for how we did this for the receiver side, and we can follow a lot of the same patterns.  Key points being the newly arriving packets go into the lock free ring, which is essentially a buffer to avoid the need to lock when new packets are arriving.  Then we use the event loop to control the work flow, so that we don't need to lock.

Here's roughly the plan I'm thinking about.

In section "#### 3.4.1 Packet Lifecycle State Machine", we currently Push(p) to the packetList.  I'm thinking that instead of this we can have another lock free ring, SendPacketRing.  This will need to have a single shard, because we want to preserve order.  This will mean the Push(p) won't need a lock and will be very fast.

Then and this is also like the reciever, we have a single consumer that will read from the SendPacketRing, and InsertOrReplace into a sendPacketBtree using the same style btree we use for the (receiver) packet btree.  The btree might seem like over kill for an order list, but the reason is that in nakLockedHonorOrder(), the current "for e := s.lossList.Front(); e != nil; e = e.Next()" is very inefficient.  The other advantage of the sendPacketBtree, is that we won't need to move packets from the packetList to the lossList.  Instead, packets enter the sendPacketBtree, and can stay in the same ordered btree the entire time.

With respect to the sendPacketBtree, there are essentially x2 points we need to track in the position of the btree, and one of these is the .Min() point:
a)
When we receive the ACKs from the reciever, this is telling us were the reciever has contigious packets up to.  We can perform the btree SendPacketRing .DeleteMin() until we have deleted all the packets the receiver has confirmed it has recieved.  This will be high performance.  Doc ack_optimization_implementation.md, "RemoveAll Optimization" describes how we make this efficient for the receive packet btree.
b)
The other point is to be use for scanning the btree to look for packets to deliver.  This will be the DeliveryStartPoint.  When the first packet is inserted into the sendPacketBtree, we will set the DeliveryStartPoint will be set to this point.  When we need to run the deliver packets function, it will start from DeliveryScanPoint and scan forward while PktTsbpdTime <= now, delivering any packets that are ready to be delivered.  It will record the point it was up to DeliveryStartPoint, so that the scanning will be efficient. e.g. Each time we need to scan, we will start knowing were we were up to, and so won't need to scan very far at all.
These points are slightly different to the receiver side, because reciever side is tracking the contigiousPoint, and tooRecentThreshold, as described in ack_optimization_plan.md section "### 3.2 Unified Scan Window Visualization".


When any NAKs arrive, looking up each NAK entry in the NAK packet will use the sendPacketBtree btree lookup, so it will be faster than the current linear search.

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
Section "#### 3.4.4 Moving Packets: packetList → lossList (on send)" descibes how the packets currently move between different lists, which we can essentially replace with a single btree, with multiple tracking points.  This new design will be replacing this movement between lists, so it should be a lot more efficient.


For a memory reuse/lifetime perspective, we are going to want to use the same ideas as we did for the io_uring and packet btree.  The retransmission_and_nak_suppression_design.md section "#### 3.4.1 Packet Lifecycle State Machine", describes the current lifecycle, and we are going to want to redesign this.  In the IO_Uring_read_path.md design describes a lot of this, and zero_copy_opportunities.md section "### Proposed: Zero-Copy Buffer Reuse" and packet_pooling_optimization.md designs describes, the zero copy changes we made, including the recvBufferPool used to read in packets from syscall or io_uring, put that packet into the packet.go packet, with zero copy.  For the sender, zero copy could be a little more tricky.  For any application that is trying to use the goSRT library, I guess when the application creates the gosrt server, the gosrt sender could create a sync.pool for the payload, and the application could do a .Get() to get the buffer, and the application could decide how it want to populate the data.  Then when the application calls the Push(p), like we do on the reciever, the we would get a packet from the packet sync.pool, populate the pointer to the payload (zero copy), and when we free the packet when it get's ACKed, the payload would be returned to the sync.pool, and then the packet returned to the sync.pool, via the decommission method. - For the design, we can refer to how ./contrib/client-generator/main.go will need to be changed to support this new design.

This new design, we want to have non locked versions of all the functions, and then support the wrapped locked versions that just do the lock, defer unlock, and call the non-locked functions e.g. pushLocked will become push, and the pushLocked will just 
    blah.Lock()
    defer blah.Unlock()
    push(p)
    

We need to carefully design this, documenting it clearly with .go files, function names, and line numbers.

We will be adding new configuration options to ./config.go, so like many other features, we will be able to feature flag in these new features, and operators will have flexible configuration options to suite their environment.  To make this more simple, I don't think we need to be completely backwards compatible.  For example, we don't need to keep the existing packetList *list.List, and lossList   *list.List, because the btree will be more efficient.  But we do want to support the Tick() with locking, verse the lock-free.  We already have lots of config.go options, so we should use a similar naming pattern.  When adding new config.go options, we also need to add new ./contrib/commmon/flags.go entries, and to make sure "make test-flags" passes.  We need to consider sensible defaults, and have input validation.

We need to consider which metrics we will need to add ./metrics/metrics.go metrics struct.  For any metrics being added, they will also need to be added to the prometheus ./metrics/handler.go ./metrics/handler_test.go, and to pass the "make audit-metrics".

We also need to consider new unit tests, table driven tests, benchmarks and race tests.
