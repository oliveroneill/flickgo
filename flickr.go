// Flickr library for Go.
// Created to be used primarily in Google App Engine.
package flickgo

import (
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Flickr API permission levels.  See
// http://www.flickr.com/services/api/auth.spec.html.
const (
	ReadPerm   = "read"
	WritePerm  = "write"
	DeletePerm = "delete"
)

// Debug logger.
type Debugfer interface {
	// Debugf formats its arguments according to the format, analogous to fmt.Printf,
	// and records the text as a log message at Debug level.
	Debugf(format string, args ...interface{})
}

// Flickr client.
type Client struct {
	// Auth token for acting on behalf of a user.
	AuthToken string

	// Logger to use.
	// Hint: App engine's Context implements this interface.
	Logger Debugfer

	// API key for your app.
	apiKey string

	// API secret for your app.
	secret string

	// Client to use for HTTP communication.
	httpClient *http.Client

	// Prevent exceeding the Flickr limit of 3600 requests per hour.
	mu          sync.Mutex
	lastRequest time.Time
}

const requestPeriod = time.Second

// Creates a new Client object.  See
// http://www.flickr.com/services/api/misc.api_keys.html for learning about API
// key and secret.  For App Engine apps, you can create httpClient by calling
// urlfetch.Client function; other apps can pass http.DefaultClient.
func New(apiKey string, secret string, httpClient *http.Client) *Client {
	return &Client{
		apiKey:     apiKey,
		secret:     secret,
		httpClient: httpClient,
	}
}

// Returns the URL for requesting authorisation to access the user's Flickr
// account.  List of possible permissions are defined at
// http://www.flickr.com/services/api/auth.spec.html.  You can also use one of
// the following constants:
//     ReadPerm
//     WritePerm
//     DeletePerm
func (c *Client) AuthURL(perms string) string {
	args := map[string]string{}
	args["perms"] = perms
	return signedURL(c.secret, c.apiKey, "auth", args)
}

// Returns the signed URL for Flickr's flickr.auth.getToken request.
func getTokenURL(c *Client, frob string) string {
	return makeURL(c, "flickr.auth.getToken", map[string]string{"frob": frob}, true)
}

type flickrError struct {
	Code string `xml:"code,attr"`
	Msg  string `xml:"msg,attr"`
}

func (e *flickrError) Err() error {
	return fmt.Errorf("Flickr error code %s: %s", e.Code, e.Msg)
}

// Exchanges a temporary frob for a token that's valid forever.
// See http://www.flickr.com/services/api/auth.howto.web.html.
func (c *Client) GetToken(frob string) (string, *User, error) {
	r := struct {
		Stat string      `xml:"stat,attr"`
		Err  flickrError `xml:"err"`
		Auth struct {
			Token string `xml:"token"`
			User  User   `xml:"user"`
		} `xml:"auth"`
	}{}
	if err := flickrGet(c, getTokenURL(c, frob), &r); err != nil {
		return "", nil, err
	}
	if r.Stat != "ok" {
		return "", nil, r.Err.Err()
	}
	return r.Auth.Token, &r.Auth.User, nil
}

// Returns URL for Flickr photo search.
func searchURL(c *Client, args map[string]string) string {
	argsCopy := clone(args)
	argsCopy["extras"] += ",url_t"
	return makeURL(c, "flickr.photos.search", argsCopy, true)
}

type PhotosSearchParams struct {
	// The NSID of the user who's photo to search. If this parameter isn't passed then everybody's public photos will be searched. A value of "me" will search against the calling user's photos for authenticated calls.
	UserID string `mapper:"user_id"`

	// A comma-delimited list of tags. Photos with one or more of the tags listed will be returned. You can exclude results that match a term by prepending it with a - character.
	Tags string `mapper:"tags"`

	// Either 'any' for an OR combination of tags, or 'all' for an AND combination. Defaults to 'any' if not specified.
	TagMode string `mapper:"tag_mode"`

	// A free text search. Photos who's title, description or tags contain the text will be returned. You can exclude results that match a term by prepending it with a - character.
	Text string `mapper:"text"`

	// Minimum upload date. Photos with an upload date greater than or equal to this value will be returned. The date can be in the form of a unix timestamp or mysql datetime.
	MinUploadDate time.Time `mapper:"min_upload_date"`

	// Maximum upload date. Photos with an upload date less than or equal to this value will be returned. The date can be in the form of a unix timestamp or mysql datetime.
	MaxUploadDate time.Time `mapper:"max_upload_date"`

	// Minimum taken date. Photos with an taken date greater than or equal to this value will be returned. The date can be in the form of a mysql datetime or unix timestamp.
	MinTakenDate time.Time `mapper:"min_taken_date"`

	// Maximum taken date. Photos with an taken date less than or equal to this value will be returned. The date can be in the form of a mysql datetime or unix timestamp.
	MaxTakenDate time.Time `mapper:"max_taken_date"`

	// The license id for photos (for possible values see the flickr.photos.licenses.getInfo method). Multiple licenses may be comma-separated.
	License string `mapper:"license"`

	// The order in which to sort returned photos. Deafults to date-posted-desc (unless you are doing a radial geo query, in which case the default sorting is by ascending distance from the point specified). The possible values are: date-posted-asc, date-posted-desc, date-taken-asc, date-taken-desc, interestingness-desc, interestingness-asc, and relevance.
	Sort string `mapper:"sort"`

	// privacy_filter (Optional)
	// Return photos only matching a certain privacy level. This only applies when making an authenticated call to view photos you own. Valid values are:
	// 1 public photos
	// 2 private photos visible to friends
	// 3 private photos visible to family
	// 4 private photos visible to friends & family
	// 5 completely private photos
	PrivacyFilter int `mapper:"privacy_filter"`

	// A comma-delimited list of 4 values defining the Bounding Box of the area that will be searched.
	//
	// The 4 values represent the bottom-left corner of the box and the top-right corner, minimum_longitude, minimum_latitude, maximum_longitude, maximum_latitude.
	//
	// Longitude has a range of -180 to 180 , latitude of -90 to 90. Defaults to -180, -90, 180, 90 if not specified.
	//
	// Unlike standard photo queries, geo (or bounding box) queries will only return 250 results per page.
	//
	// Geo queries require some sort of limiting agent in order to prevent the database from crying. This is basically like the check against "parameterless searches" for queries without a geo component.
	//
	// A tag, for instance, is considered a limiting agent as are user defined min_date_taken and min_date_upload parameters — If no limiting factor is passed we return only photos added in the last 12 hours (though we may extend the limit in the future).
	BBox string `mapper:"bbox"`

	// Recorded accuracy level of the location information. Current range is 1-16 :
	// World level is 1
	// Country is ~3
	// Region is ~6
	// City is ~11
	// Street is ~16
	// Defaults to maximum value if not specified.
	Accuracy int `mapper:"accuracy"`

	// Safe search setting:
	// 1 for safe.
	// 2 for moderate.
	// 3 for restricted.
	// (Please note: Un-authed calls can only see Safe content.)
	SafeSearch int `mapper:"safe_search"`

	// Content Type setting:
	// 1 for photos only.
	// 2 for screenshots only.
	// 3 for 'other' only.
	// 4 for photos and screenshots.
	// 5 for screenshots and 'other'.
	// 6 for photos and 'other'.
	// 7 for photos, screenshots, and 'other' (all).
	ContentType int `mapper:"content_type"`

	// Aside from passing in a fully formed machine tag, there is a special syntax for searching on specific properties :
	// Find photos using the 'dc' namespace : "machine_tags" => "dc:"
	// Find photos with a title in the 'dc' namespace : "machine_tags" => "dc:title="
	// Find photos titled "mr. camera" in the 'dc' namespace : "machine_tags" => "dc:title=\"mr. camera\"
	// Find photos whose value is "mr. camera" : "machine_tags" => "*:*=\"mr. camera\""
	// Find photos that have a title, in any namespace : "machine_tags" => "*:title="
	// Find photos that have a title, in any namespace, whose value is "mr. camera" : "machine_tags" => "*:title=\"mr. camera\""
	// Find photos, in the 'dc' namespace whose value is "mr. camera" : "machine_tags" => "dc:*=\"mr. camera\""
	// Multiple machine tags may be queried by passing a comma-separated list. The number of machine tags you can pass in a single query depends on the tag mode (AND or OR) that you are querying with. "AND" queries are limited to (16) machine tags. "OR" queries are limited to (8).
	MachineTags string `mapper:"machine_tags"`

	// Either 'any' for an OR combination of tags, or 'all' for an AND combination. Defaults to 'any' if not specified.
	MachineTagMode string `mapper:"machine_tag_mode"`

	// The id of a group who's pool to search. If specified, only matching photos posted to the group's pool will be returned.
	GroupID string `mapper:"group_id"`

	// Search your contacts. Either 'all' or 'ff' for just friends and family. (Experimental)
	Contacts string `mapper:"contacts"`

	// A 32-bit identifier that uniquely represents spatial entities. (not used if bbox argument is present).
	//
	// Geo queries require some sort of limiting agent in order to prevent the database from crying. This is basically like the check against "parameterless searches" for queries without a geo component.
	//
	// A tag, for instance, is considered a limiting agent as are user defined min_date_taken and min_date_upload parameters — If no limiting factor is passed we return only photos added in the last 12 hours (though we may extend the limit in the future).
	WoeID string `mapper:"woe_id"`

	// A Flickr place id. (not used if bbox argument is present).
	//
	// Geo queries require some sort of limiting agent in order to prevent the database from crying. This is basically like the check against "parameterless searches" for queries without a geo component.
	//
	// A tag, for instance, is considered a limiting agent as are user defined min_date_taken and min_date_upload parameters — If no limiting factor is passed we return only photos added in the last 12 hours (though we may extend the limit in the future).
	PlaceID string `mapper:"place_id"`

	// Filter results by media type. Possible values are all (default), photos or videos
	Media string `mapper:"media"`

	// Any photo that has been geotagged, or if the value is "0" any photo that has not been geotagged.
	//
	// Geo queries require some sort of limiting agent in order to prevent the database from crying. This is basically like the check against "parameterless searches" for queries without a geo component.
	//
	// A tag, for instance, is considered a limiting agent as are user defined min_date_taken and min_date_upload parameters — If no limiting factor is passed we return only photos added in the last 12 hours (though we may extend the limit in the future).
	HasGeo string `mapper:"has_geo"`

	// Geo context is a numeric value representing the photo's geotagginess beyond latitude and longitude. For example, you may wish to search for photos that were taken "indoors" or "outdoors".
	//
	// The current list of context IDs is :
	//
	// 0, not defined.
	// 1, indoors.
	// 2, outdoors.
	//
	//
	// Geo queries require some sort of limiting agent in order to prevent the database from crying. This is basically like the check against "parameterless searches" for queries without a geo component.
	//
	// A tag, for instance, is considered a limiting agent as are user defined min_date_taken and min_date_upload parameters — If no limiting factor is passed we return only photos added in the last 12 hours (though we may extend the limit in the future).
	GeoContext string `mapper:"geo_context"`

	// A valid latitude, in decimal format, for doing radial geo queries.
	//
	// Geo queries require some sort of limiting agent in order to prevent the database from crying. This is basically like the check against "parameterless searches" for queries without a geo component.
	//
	// A tag, for instance, is considered a limiting agent as are user defined min_date_taken and min_date_upload parameters — If no limiting factor is passed we return only photos added in the last 12 hours (though we may extend the limit in the future).
	Lat string `mapper:"lat"`

	// A valid longitude, in decimal format, for doing radial geo queries.
	//
	// Geo queries require some sort of limiting agent in order to prevent the database from crying. This is basically like the check against "parameterless searches" for queries without a geo component.
	//
	// A tag, for instance, is considered a limiting agent as are user defined min_date_taken and min_date_upload parameters — If no limiting factor is passed we return only photos added in the last 12 hours (though we may extend the limit in the future).
	Lon string `mapper:"lon"`

	// A valid radius used for geo queries, greater than zero and less than 20 miles (or 32 kilometers), for use with point-based geo queries. The default value is 5 (km).
	Radius string `mapper:"radius"`

	// The unit of measure when doing radial geo queries. Valid options are "mi" (miles) and "km" (kilometers). The default is "km".
	RadiusUnits string `mapper:"radius_units"`

	// Limit the scope of the search to only photos that are part of the Flickr Commons project. Default is false.
	IsCommons string `mapper:"is_commons"`

	// Limit the scope of the search to only photos that are in a gallery? Default is false, search all photos.
	InGallery string `mapper:"in_gallery"`

	// Limit the scope of the search to only photos that are for sale on Getty. Default is false.
	IsGetty string `mapper:"is_getty"`

	// A comma-delimited list of extra information to fetch for each returned record. Currently supported fields are: description, license, date_upload, date_taken, owner_name, icon_server, original_format, last_update, geo, tags, machine_tags, o_dims, views, media, path_alias, url_sq, url_t, url_s, url_q, url_m, url_n, url_z, url_c, url_l, url_o
	Extras string `mapper:"extras"`

	// Number of photos to return per page. If this argument is omitted, it defaults to 100. The maximum allowed value is 500.
	PerPage int `mapper:"per_page"`

	// Which page of results to return.
	Page int `mapper:"page"`
}

// Searches for photos.  args contains search parameters as described in
// http://www.flickr.com/services/api/flickr.photos.search.html.
func (c *Client) PhotosSearch(params PhotosSearchParams) (*SearchResponse, error) {
	r := struct {
		Stat   string         `xml:"stat,attr"`
		Err    flickrError    `xml:"err"`
		Photos SearchResponse `xml:"photos"`
	}{}
	if err := flickrGet(c, makeURL(c, "flickr.photos.search", StructToMap(params), true), &r); err != nil {
		return nil, err
	}
	if r.Stat != "ok" {
		return nil, r.Err.Err()
	}

	for i, ph := range r.Photos.Photos {
		h, hErr := strconv.ParseFloat(ph.HeightT, 64)
		w, wErr := strconv.ParseFloat(ph.WidthT, 64)
		if hErr == nil && wErr == nil {
			// ph is apparently just a copy of r.Photos.Photos[i], so we are
			// updating the original.
			r.Photos.Photos[i].Ratio = w / h
		}
	}
	return &r.Photos, nil
}

type ContactsGetPublicListParams struct {
	// The NSID of the user to fetch the contact list for.
	UserID string `mapper:"user_id"`

	// Number of photos to return per page. If this argument is omitted, it defaults to 1000. The maximum allowed value is 1000.
	PerPage int `mapper:"per_page"`

	// The page of results to return. If this argument is omitted, it defaults to 1.
	Page int `mapper:"page"`
}

// Get the contact list for a user.  args contains search parameters as described in
// http://www.flickr.com/services/api/flickr.contacts.getPublicList.html.
func (c *Client) ContactsGetPublicList(params ContactsGetPublicListParams) (*ContactsGetPublicListResponse, error) {
	r := struct {
		Stat     string                        `xml:"stat,attr"`
		Err      flickrError                   `xml:"err"`
		Contacts ContactsGetPublicListResponse `xml:"contacts"`
	}{}
	if err := flickrGet(c, makeURL(c, "flickr.contacts.getPublicList", StructToMap(params), true), &r); err != nil {
		return nil, err
	}
	if r.Stat != "ok" {
		return nil, r.Err.Err()
	}
	return &r.Contacts, nil
}

// // Initiates an asynchronous photo upload and returns the ticket ID.  See
// // http://www.flickr.com/services/api/upload.async.html for details.
// func (c *Client) Upload(name string, photo []byte,
// 	args map[string]string) (ticketID string, err error) {
// 	req, uErr := uploadRequest(c, name, photo, args)
// 	if uErr != nil {
// 		return "", wrapErr("request creation failed", uErr)
// 	}

// 	resp := struct {
// 		Stat     string      `xml:"stat,attr"`
// 		Err      flickrError `xml:"err"`
// 		TicketID string      `xml:"ticketid"`
// 	}{}
// 	if err := flickrPost(c, req, &resp); err != nil {
// 		return "", wrapErr("uploading failed", err)
// 	}
// 	if resp.Stat != "ok" {
// 		return "", resp.Err.Err()
// 	}
// 	return resp.TicketID, nil
// }

// // Returns URL for flickr.photos.upload.checkTickets request.
// func checkTicketsURL(c *Client, tickets []string) string {
// 	args := make(map[string]string)
// 	args["tickets"] = strings.Join(tickets, ",")
// 	return makeURL(c, "flickr.photos.upload.checkTickets", args, false)
// }

// // Asynchronous photo upload status response.
// type TicketStatus struct {
// 	ID       string `xml:"id,attr"`
// 	Complete string `xml:"complete,attr"`
// 	Invalid  string `xml:"invalid,attr"`
// 	PhotoID  string `xml:"photoid,attr"`
// }

// // Checks the status of async upload tickets (returned by Upload method, for
// // example).  Interface for
// // http://www.flickr.com/services/api/flickr.photos.upload.checkTickets.html
// // API method.
// func (c *Client) CheckTickets(tickets []string) (statuses []TicketStatus, err error) {
// 	r := struct {
// 		Stat    string         `xml:"stat,attr"`
// 		Err     flickrError    `xml:"err"`
// 		Tickets []TicketStatus `xml:"uploader>ticket"`
// 	}{}
// 	if err := flickrGet(c, checkTicketsURL(c, tickets), &r); err != nil {
// 		return nil, err
// 	}
// 	if r.Stat != "ok" {
// 		return nil, r.Err.Err()
// 	}
// 	return r.Tickets, nil
// }

// // Returns URL for flickr.photosets.getList request.
// func getPhotoSetsURL(c *Client, userID string) string {
// 	args := make(map[string]string)
// 	args["user_id"] = userID
// 	return makeURL(c, "flickr.photosets.getList", args, true)
// }

// // Returns the list of photo sets of the specified user.
// func (c *Client) GetSets(userID string) ([]PhotoSet, error) {
// 	r := struct {
// 		Stat string      `xml:"stat,attr"`
// 		Err  flickrError `xml:"err"`
// 		Sets []PhotoSet  `xml:"photosets>photoset"`
// 	}{}
// 	if err := flickrGet(c, getPhotoSetsURL(c, userID), &r); err != nil {
// 		return nil, err
// 	}
// 	if r.Stat != "ok" {
// 		return nil, r.Err.Err()
// 	}
// 	return r.Sets, nil
// }

// func addToSetURL(c *Client, photoID, setID string) string {
// 	args := make(map[string]string)
// 	args["photo_id"] = photoID
// 	args["photoset_id"] = setID
// 	return makeURL(c, "flickr.photosets.addPhoto", args, true)
// }

// // Adds a photo to a photoset.
// func (c *Client) AddPhotoToSet(photoID, setID string) error {
// 	r := struct {
// 		Stat string      `xml:"stat,attr"`
// 		Err  flickrError `xml:"err"`
// 	}{}
// 	if err := flickrGet(c, addToSetURL(c, photoID, setID), &r); err != nil {
// 		return err
// 	}
// 	if r.Stat != "ok" {
// 		return r.Err.Err()
// 	}
// 	return nil
// }

// func getLocationURL(c *Client, args map[string]string) string {
// 	argsCopy := clone(args)
// 	return makeURL(c, "flickr.photos.geo.getLocation", argsCopy, true)
// }

// // Implements https://www.flickr.com/services/api/flickr.photos.geo.getLocation.html
// func (c *Client) GetLocation(args map[string]string) (*LocationResponse, error) {
// 	r := struct {
// 		Stat     string           `xml:"stat,attr"`
// 		Err      flickrError      `xml:"err"`
// 		Location LocationResponse `xml:"photo"`
// 	}{}
// 	if err := flickrGet(c, getLocationURL(c, args), &r); err != nil {
// 		return nil, err
// 	}

// 	if r.Stat != "ok" {
// 		return nil, r.Err.Err()
// 	}

// 	return &r.Location, nil
// }

type PeopleGetInfoParams struct {
	UserID string `mapper:"user_id"`
}

// Implements https://www.flickr.com/services/api/flickr.people.getInfo.html
func (c *Client) PeopleGetInfo(params PeopleGetInfoParams) (*PersonResponse, error) {
	r := struct {
		Stat   string         `xml:"stat,attr"`
		Err    flickrError    `xml:"err"`
		Person PersonResponse `xml:"person"`
	}{}
	if err := flickrGet(c, makeURL(c, "flickr.people.getInfo", StructToMap(params), true), &r); err != nil {
		return nil, err
	}

	if r.Stat != "ok" {
		return nil, r.Err.Err()
	}

	return &r.Person, nil
}

type PhotosGetInfoParams struct {
	PhotoID string `mapper:"photo_id"`
	Secret  int    `mapper:"secret"`
}

// Implements https://www.flickr.com/services/api/flickr.photos.getInfo.html
func (c *Client) PhotosGetInfo(params PhotosGetInfoParams) (*PhotoInfoResponse, error) {
	r := struct {
		Stat string      `xml:"stat,attr"`
		Err  flickrError `xml:"err"`
		PhotoInfoResponse
	}{}
	if err := flickrGet(c, makeURL(c, "flickr.photos.getInfo", StructToMap(params), true), &r); err != nil {
		return nil, err
	}
	if r.Stat != "ok" {
		return nil, r.Err.Err()
	}

	return &r.PhotoInfoResponse, nil
}

type PhotosGetFavoritesParams struct {
	PhotoID string `mapper:"photo_id"`
	Page    int    `mapper:"page"`
	PerPage int    `mapper:"per_page"`
}

// Implements https://www.flickr.com/services/api/flickr.photos.getFavorites.html
func (c *Client) PhotosGetFavorites(params PhotosGetFavoritesParams) (*PhotoFavoritesResponse, error) {
	r := struct {
		Stat  string                 `xml:"stat,attr"`
		Err   flickrError            `xml:"err"`
		Faves PhotoFavoritesResponse `xml:"photo"`
	}{}
	if err := flickrGet(c, makeURL(c, "flickr.photos.getFavorites", StructToMap(params), true), &r); err != nil {
		return nil, err
	}
	if r.Stat != "ok" {
		return nil, r.Err.Err()
	}

	return &r.Faves, nil
}

func pushSubscribeURL(c *Client, args map[string]string) string {
	argsCopy := clone(args)
	return makeURL(c, "flickr.push.subscribe", argsCopy, true)
}

// Implements https://api.flickr.com/services/rest/?method=flickr.push.subscribe
func (c *Client) PushSubscribe(args map[string]string) error {
	r := struct {
		Stat string      `xml:"stat,attr"`
		Err  flickrError `xml:"err"`
	}{}
	if err := flickrGet(c, pushSubscribeURL(c, args), &r); err != nil {
		return err
	}
	if r.Stat != "ok" {
		return r.Err.Err()
	}

	return nil
}

var mapperTagRE = regexp.MustCompile(`\bmapper:"([^"]*)`)

func StructToMap(v interface{}) map[string]string {
	r := make(map[string]string)
	val := reflect.ValueOf(v)
	if val.Type().Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if val.Type().Kind() != reflect.Struct {
		panic("can only call StructToMap on a struct or a pointer to a struct")
	}
	t := val.Type()
	for i := 0; i < t.NumField(); i++ {
		val := val.Field(i)
		field := t.Field(i)
		if field.Anonymous || !val.CanInterface() {
			continue
		}
		switch field.Type.Kind() {
		case reflect.Int:
		case reflect.Int8:
		case reflect.Int16:
		case reflect.Int32:
		case reflect.Int64:
		case reflect.Uint:
		case reflect.Uint8:
		case reflect.Uint16:
		case reflect.Uint32:
		case reflect.Uint64:
		case reflect.Float32:
		case reflect.Float64:
		case reflect.String:
		default:
			switch field.Type {
			case reflect.TypeOf(time.Time{}):
			default:
				continue
			}
		}
		z := reflect.Zero(field.Type)
		if reflect.DeepEqual(val.Interface(), z.Interface()) {
			continue
		}
		name := field.Name
		if m := mapperTagRE.FindStringSubmatch(string(t.Field(i).Tag)); len(m) == 2 {
			name = m[1]
		}
		var str string
		switch field.Type {
		case reflect.TypeOf(time.Time{}):
			str = fmt.Sprintf("%d", val.Interface().(time.Time).UnixNano()/1e9)
		default:
			str = fmt.Sprintf("%v", val.Interface())
		}
		r[name] = str
	}
	return r
}
