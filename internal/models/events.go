package models

import (
	"clearlove/internal/database"
	"clearlove/internal/util"
)

// AddEvent 记录一条通知事件，供 Android 客户端轮询。
// target_id=0 表示全站新帖；其余类型指向帖子作者。
// ref_id 在评论事件中为评论 ID，在投币事件中为硬币数。
func AddEvent(kind string, actorID, targetID, postID, refID int64) {
	if database.DB == nil {
		return
	}
	_, err := database.DB.Exec(
		"INSERT INTO events(kind,actor_id,target_id,post_id,ref_id,created_at) VALUES(?,?,?,?,?,?)",
		kind, actorID, targetID, postID, refID, Now())
	if err != nil {
		util.Log("warn", "写入通知事件失败（%s #%d）: %v", kind, postID, err)
	}
}
