**Why Does Net() Exist**
- See once we expand / explode our bom we get a list of items that we need to buy for the final product. 
- Example 
    - Lets say we need to buy 100 screws
    - maybe 50 bolts
    - 10 lamp bases.
- Now this is the total required amount needed to create the orignal production plan. but if you think about you don't have to buy all the parts honestly thats a waste. because you might have your own inventory.
- so the actual amount you have to buy is.
    - **net = total - onhand**
- After we find the net, logically speaking we need to make a purchase order request for those items.
- So thats what net does.

**Algorithm For Net()**
- first the map of item to its qty consists of item id.
- get the actual item data.
    - Now for the item data. get the total quantity present currently - Compute the onhand
    - Calclate the the net = total - onhand + stocksafety.
    - if net <= 0 which means the on hand is enough that we dont need to create purchase requests.
    - if item as fixedLotSize then we have to round the net = ceil(net/fixed) * fixed.
    - Once the net is determined, create and insert purchase order requests.
