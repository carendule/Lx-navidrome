-- +goose Up
alter table user add online_search_enabled boolean not null default true;
alter table user add online_browser_download_enabled boolean not null default true;
alter table user add online_server_download_enabled boolean not null default true;
alter table user add online_playlist_sync_enabled boolean not null default true;
alter table playlist add visible_to_user_ids text not null default '[]';
update playlist set visible_to_user_ids = (select json_group_array(id) from user) where public = true;
update playlist set public = false;

-- +goose Down
update playlist set public = true where json_array_length(visible_to_user_ids) > 0;
alter table playlist drop column visible_to_user_ids;
alter table user drop column online_playlist_sync_enabled;
alter table user drop column online_server_download_enabled;
alter table user drop column online_browser_download_enabled;
alter table user drop column online_search_enabled;